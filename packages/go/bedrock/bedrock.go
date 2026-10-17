package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"scriptbridge/bridge"
)

// Disconnect reasons reported in State.Disconnect.Reason.
const (
	ReasonMinecraftClosed  = "MINECRAFT_CLOSED"
	ReasonConnectionLost   = "CONNECTION_LOST"
	ReasonMessageTooLarge  = "MESSAGE_TOO_LARGE"
	ReasonWriteFailed      = "WRITE_FAILED"
	ReasonHeartbeatTimeout = "HEARTBEAT_TIMEOUT"
	ReasonServerStopped    = "SERVER_STOPPED"
)

// DefaultHeartbeat is the ping interval used when Config.Heartbeat is zero.
// Each ping prints one reply in Minecraft chat, like other add-on frames.
const DefaultHeartbeat = 30 * time.Second

// maxMissedHeartbeats ends a session after this many unanswered pings in a row.
const maxMissedHeartbeats = 3

var guidance = map[string]string{
	ReasonMinecraftClosed:  "Minecraft closed the WebSocket. Rejoin the world, then run /wsserver with this server's address.",
	ReasonConnectionLost:   "The connection dropped. Check that Minecraft is still running on this machine, then run /wsserver again.",
	ReasonMessageTooLarge:  "Minecraft sent a message larger than this server accepts. Reconnect with /wsserver, and keep add-on messages small.",
	ReasonWriteFailed:      "Commands could not be sent to Minecraft. Check that Minecraft is still running, then run /wsserver again.",
	ReasonHeartbeatTimeout: "Minecraft is connected, but the add-on stopped answering. Enable or reload the behavior pack, check Minecraft's content log for script errors, then run /wsserver again.",
	ReasonServerStopped:    "This server stopped the connection. Restart the server, then run /wsserver again.",
}

type Config struct {
	Namespace string
	Prefix    string
	Timeout   time.Duration
	// Heartbeat is how often the server pings the add-on. Zero uses DefaultHeartbeat; negative disables pings.
	Heartbeat time.Duration
}

type State struct {
	Connected   bool        `json:"connected"`
	Ready       bool        `json:"ready"`
	Error       string      `json:"error,omitempty"`
	ConnectedAt int64       `json:"connectedAt,omitempty"` // Unix milliseconds
	Health      Health      `json:"health"`
	Disconnect  *Disconnect `json:"disconnect,omitempty"` // Why the most recent connection ended
}

// Health reports liveness for the current connection.
type Health struct {
	LastSeen         int64 `json:"lastSeen,omitempty"` // Unix milliseconds of the last packet from Minecraft
	LatencyMs        int64 `json:"latencyMs"`          // Round trip of the last answered heartbeat
	MissedHeartbeats int   `json:"missedHeartbeats"`   // Consecutive unanswered heartbeats
}

// Disconnect explains why a connection ended and what to do next.
type Disconnect struct {
	Reason   string `json:"reason"`
	Code     int    `json:"code,omitempty"` // WebSocket close code, when Minecraft sent one
	Message  string `json:"message"`
	Guidance string `json:"guidance"`
	At       int64  `json:"at"` // Unix milliseconds
}

type Server struct {
	Register func(*bridge.Bridge)
	Observe  func(string, any)
	config   Config
	mu       sync.Mutex
	active   *session
	state    State
}

type session struct {
	conn      *websocket.Conn
	peer      *bridge.Bridge
	ctx       context.Context
	cancel    context.CancelFunc
	writer    chan struct{}
	sequence  atomic.Uint64
	namespace string
	prefix    string
	endMu     sync.Mutex
	ended     *Disconnect
}

type packet struct {
	Header struct {
		RequestID      string `json:"requestId"`
		MessagePurpose string `json:"messagePurpose"`
		EventName      string `json:"eventName"`
	} `json:"header"`
	Body json.RawMessage `json:"body"`
}

func New(config Config) (*Server, error) {
	if config.Namespace == "" {
		config.Namespace = "scaffold"
	}
	if config.Prefix == "" {
		config.Prefix = "§8[SC] "
	}
	if !regexp.MustCompile(`^[a-z0-9_.-]+$`).MatchString(config.Namespace) || strings.ContainsAny(config.Prefix, "\r\n") || config.Timeout < 0 {
		return nil, fmt.Errorf("expected a Minecraft namespace, a single-line prefix, and a positive timeout")
	}
	return &Server{config: config}, nil
}

// State returns a snapshot of the connection, health, and last disconnect.
func (s *Server) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Server) Request(ctx context.Context, name string, data any) (json.RawMessage, error) {
	active, err := s.ready()
	if err != nil {
		return nil, err
	}
	return active.peer.Request(ctx, name, data)
}

// Close ends the current connection, if any. The disconnect reason is SERVER_STOPPED.
func (s *Server) Close() {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != nil {
		active.fail(disconnect(ReasonServerStopped, 0, "The server stopped"))
	}
}

func (s *Server) Emit(ctx context.Context, name string, data any) error {
	active, err := s.ready()
	if err != nil {
		return err
	}
	return active.peer.Emit(ctx, name, data)
}

// ready returns the active session when the add-on handshake has completed.
// Otherwise it returns a NOT_READY error that says what to fix.
func (s *Server) ready() (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.active == nil:
		return nil, &bridge.Error{Code: bridge.CodeNotReady, Message: "Minecraft is not connected. Run /wsserver with this server's address."}
	case s.state.Error != "":
		return nil, &bridge.Error{Code: bridge.CodeNotReady, Message: "Add-on handshake failed: " + s.state.Error}
	case !s.state.Ready:
		return nil, &bridge.Error{Code: bridge.CodeNotReady, Message: "Add-on handshake is pending. Enable or reload the behavior pack, then wait for Minecraft ready."}
	}
	return s.active, nil
}

func (s *Server) Handler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		http.Error(w, "This endpoint is for Minecraft", http.StatusForbidden)
		return
	}
	if strings.Contains(r.Header.Get("Sec-WebSocket-Protocol"), "com.microsoft.minecraft.wsencrypt") {
		http.Error(w, "Turn off Encrypted Websockets in Minecraft settings", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if s.active != nil {
		s.mu.Unlock()
		http.Error(w, "A world is already connected", http.StatusConflict)
		return
	}
	upgrader := websocket.Upgrader{}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	active := &session{conn: conn, ctx: ctx, cancel: cancel, writer: make(chan struct{}, 1), namespace: s.config.Namespace, prefix: s.config.Prefix}
	active.peer = bridge.New(active.send, bridge.Options{
		Timeout: s.config.Timeout,
		Observe: func(direction string, frame bridge.Frame) {
			s.publish("frame", map[string]any{"direction": direction, "frame": frame})
		},
		OnError: func(err error) { s.publish("error", bridge.Failure(err)) },
	})
	if s.Register != nil {
		s.Register(active.peer)
	}
	hello := make(chan struct{}, 1)
	hello <- struct{}{}
	active.peer.On("bridge.started", func(json.RawMessage) {
		select {
		case hello <- struct{}{}:
		default:
		}
	})
	s.active, s.state = active, State{Connected: true, ConnectedAt: time.Now().UnixMilli()}
	s.mu.Unlock()
	s.publish("state", s.State())
	defer func() {
		// No-op when the session already recorded a reason; this covers setup failures.
		active.fail(disconnect(ReasonConnectionLost, 0, "The connection ended before setup finished"))
		s.mu.Lock()
		reason := active.endReason()
		s.active, s.state = nil, State{Disconnect: reason}
		s.mu.Unlock()
		s.publish("state", s.State())
	}()
	conn.SetReadLimit(16 * 1024)
	if err := active.write(ctx, "subscribe", "subscribe", map[string]string{"eventName": "PlayerMessage"}); err != nil {
		return
	}
	s.goSafe(func() { s.handshake(ctx, active, hello) })
	if interval := s.heartbeatInterval(); interval > 0 {
		s.goSafe(func() { s.heartbeat(ctx, active, interval) })
	}
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			active.fail(classify(err))
			return
		}
		s.touch(active)
		var p packet
		if json.Unmarshal(raw, &p) != nil {
			continue
		}
		switch p.Header.MessagePurpose {
		case "commandResponse":
			var result struct {
				StatusCode    int    `json:"statusCode"`
				StatusMessage string `json:"statusMessage"`
			}
			if json.Unmarshal(p.Body, &result) == nil && result.StatusCode != 0 {
				err := &bridge.Error{Code: bridge.CodeCommandRejected, Message: result.StatusMessage}
				if err.Message == "" {
					err.Message = "Minecraft rejected the command"
				}
				if id, ok := strings.CutPrefix(p.Header.RequestID, "request-"); ok {
					active.peer.Reject(id, err)
				}
				s.publish("error", err)
			}
		case "event":
			if raw := playerFrame(p, active.prefix); raw != nil {
				if err := active.peer.Receive(ctx, raw); err != nil {
					s.publish("error", bridge.Failure(err))
				}
			}
		}
	}
}

// handshake asks the add-on for its protocol version each time it announces it has started.
func (s *Server) handshake(ctx context.Context, active *session, hello <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-hello:
			result, err := active.peer.Request(ctx, "bridge.hello", nil)
			var data struct {
				Protocol int `json:"protocol"`
			}
			if err == nil && (json.Unmarshal(result, &data) != nil || data.Protocol != 1) {
				err = &bridge.Error{Code: bridge.CodeIncompatible, Message: "Add-on protocol must be version 1"}
			}
			s.mu.Lock()
			if s.active == active {
				s.state.Ready, s.state.Error = err == nil, ""
				if err != nil {
					s.state.Error = err.Error()
				}
			}
			s.mu.Unlock()
			s.publish("state", s.State())
		}
	}
}

// heartbeat pings the add-on on an interval. Any reply, including UNKNOWN_REQUEST from an
// older add-on, proves it is alive. Several misses in a row end the session.
func (s *Server) heartbeat(ctx context.Context, active *session, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		started := time.Now()
		_, err := active.peer.Request(ctx, "bridge.ping", nil)
		if ctx.Err() != nil {
			return
		}
		alive := err == nil || bridge.Failure(err).Code == bridge.CodeUnknownRequest
		s.mu.Lock()
		if s.active != active {
			s.mu.Unlock()
			return
		}
		if alive {
			s.state.Health.LatencyMs = time.Since(started).Milliseconds()
			s.state.Health.LastSeen = time.Now().UnixMilli()
			s.state.Health.MissedHeartbeats = 0
		} else {
			s.state.Health.MissedHeartbeats++
		}
		missed := s.state.Health.MissedHeartbeats
		s.mu.Unlock()
		s.publish("state", s.State())
		if missed >= maxMissedHeartbeats {
			active.fail(disconnect(ReasonHeartbeatTimeout, 0, fmt.Sprintf("The add-on missed %d heartbeats in a row", missed)))
			return
		}
	}
}

func (s *Server) heartbeatInterval() time.Duration {
	if s.config.Heartbeat == 0 {
		return DefaultHeartbeat
	}
	return s.config.Heartbeat
}

// touch records that Minecraft sent something, which is the basis for "last seen".
func (s *Server) touch(active *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == active {
		s.state.Health.LastSeen = time.Now().UnixMilli()
	}
}

// goSafe runs background work for a connection. A panic is published as an error instead of
// crashing the process, because goroutines started here are outside net/http's recovery.
func (s *Server) goSafe(work func()) {
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.publish("error", &bridge.Error{Code: bridge.CodeInternal, Message: fmt.Sprintf("Background task panicked: %v", recovered)})
			}
		}()
		work()
	}()
}

// classify turns a read failure into a disconnect reason that can be shown to the user.
func classify(err error) Disconnect {
	var closed *websocket.CloseError
	switch {
	case errors.As(err, &closed):
		message := fmt.Sprintf("Minecraft closed the WebSocket with code %d", closed.Code)
		if closed.Text != "" {
			message += ": " + closed.Text
		}
		return disconnect(ReasonMinecraftClosed, closed.Code, message)
	case errors.Is(err, websocket.ErrReadLimit):
		return disconnect(ReasonMessageTooLarge, 0, "Minecraft sent a message larger than 16 KB")
	default:
		return disconnect(ReasonConnectionLost, 0, err.Error())
	}
}

func disconnect(reason string, code int, message string) Disconnect {
	return Disconnect{Reason: reason, Code: code, Message: message, Guidance: guidance[reason], At: time.Now().UnixMilli()}
}

// fail ends the session once. The first reason wins, so a write error is not replaced by
// the read error it causes.
func (s *session) fail(reason Disconnect) {
	s.endMu.Lock()
	if s.ended != nil {
		s.endMu.Unlock()
		return
	}
	s.ended = &reason
	s.endMu.Unlock()
	s.cancel()
	s.peer.Close()
	_ = s.conn.Close()
}

func (s *session) endReason() *Disconnect {
	s.endMu.Lock()
	defer s.endMu.Unlock()
	return s.ended
}

func playerFrame(p packet, prefix string) []byte {
	var body struct {
		EventName string `json:"eventName"`
		Message   string `json:"message"`
		Sender    string `json:"sender"`
		Type      string `json:"type"`
	}
	if json.Unmarshal(p.Body, &body) != nil || (p.Header.EventName != "PlayerMessage" && body.EventName != "PlayerMessage") || body.Type != "say" || body.Sender != "Server" {
		return nil
	}
	if raw, ok := strings.CutPrefix(body.Message, prefix); ok {
		return []byte(raw)
	}
	if raw, ok := strings.CutPrefix(body.Message, "[Server] "+prefix); ok {
		return []byte(raw)
	}
	return nil
}

func (s *session) send(ctx context.Context, frame bridge.Frame) error {
	if err := ctx.Err(); err != nil {
		return &bridge.Error{Code: bridge.CodeTimeout, Message: "Request deadline elapsed"}
	}
	raw, err := bridge.Encode(frame)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("command-%d", s.sequence.Add(1))
	if frame.Kind == "request" {
		id = "request-" + frame.ID
	}
	return s.write(ctx, "commandRequest", id, map[string]any{
		"commandLine": "scriptevent " + s.namespace + ":message " + string(raw), "version": 1,
	})
}

func (s *session) write(ctx context.Context, purpose, id string, body any) error {
	select {
	case s.writer <- struct{}{}:
		defer func() { <-s.writer }()
	case <-ctx.Done():
		return &bridge.Error{Code: bridge.CodeTimeout, Message: "Request deadline elapsed"}
	case <-s.ctx.Done():
		return &bridge.Error{Code: bridge.CodeDisconnected, Message: "Minecraft disconnected"}
	}
	if s.ctx.Err() != nil {
		return &bridge.Error{Code: bridge.CodeDisconnected, Message: "Minecraft disconnected"}
	}
	if ctx.Err() != nil {
		return &bridge.Error{Code: bridge.CodeTimeout, Message: "Request deadline elapsed"}
	}
	deadline := time.Now().Add(5 * time.Second)
	if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(deadline) {
		deadline = callerDeadline
	}
	_ = s.conn.SetWriteDeadline(deadline)
	err := s.conn.WriteJSON(map[string]any{
		"header": map[string]any{"version": 1, "requestId": id, "messageType": "commandRequest", "messagePurpose": purpose},
		"body":   body,
	})
	if err != nil {
		s.fail(disconnect(ReasonWriteFailed, 0, err.Error()))
		return &bridge.Error{Code: bridge.CodeDisconnected, Message: "Could not write to Minecraft"}
	}
	return nil
}

func (s *Server) publish(kind string, data any) {
	if s.Observe != nil {
		s.Observe(kind, data)
	}
}
