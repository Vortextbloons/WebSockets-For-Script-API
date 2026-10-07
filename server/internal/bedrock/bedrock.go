package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"scriptbridge/internal/bridge"
)

const Namespace = "scaffold"
const Prefix = "§8[SC] "

type State struct {
	Connected bool   `json:"connected"`
	Ready     bool   `json:"ready"`
	Error     string `json:"error,omitempty"`
}

type Server struct {
	Register func(*bridge.Bridge)
	Observe  func(string, any)
	mu       sync.Mutex
	active   *session
	state    State
}

type session struct {
	conn     *websocket.Conn
	peer     *bridge.Bridge
	ctx      context.Context
	cancel   context.CancelFunc
	writer   chan struct{}
	sequence atomic.Uint64
}

type packet struct {
	Header struct {
		RequestID      string `json:"requestId"`
		MessagePurpose string `json:"messagePurpose"`
		EventName      string `json:"eventName"`
	} `json:"header"`
	Body json.RawMessage `json:"body"`
}

func (s *Server) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Server) Request(ctx context.Context, name string, data any) (json.RawMessage, error) {
	s.mu.Lock()
	active, ready := s.active, s.state.Ready
	s.mu.Unlock()
	if active == nil || !ready {
		return nil, &bridge.Error{Code: "NOT_READY", Message: "Connect Minecraft and wait for the add-on handshake"}
	}
	return active.peer.Request(ctx, name, data)
}

func (s *Server) Close() {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != nil {
		active.cancel()
		active.peer.Close()
		_ = active.conn.Close()
	}
}

func (s *Server) Emit(ctx context.Context, name string, data any) error {
	s.mu.Lock()
	active, ready := s.active, s.state.Ready
	s.mu.Unlock()
	if active == nil || !ready {
		return &bridge.Error{Code: "NOT_READY", Message: "Connect Minecraft and wait for the add-on handshake"}
	}
	return active.peer.Emit(ctx, name, data)
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
	active := &session{conn: conn, ctx: ctx, cancel: cancel, writer: make(chan struct{}, 1)}
	active.peer = bridge.New(active.send, func(direction string, frame bridge.Frame) {
		s.publish("frame", map[string]any{"direction": direction, "frame": frame})
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
	s.active, s.state = active, State{Connected: true}
	s.mu.Unlock()
	s.publish("state", s.State())
	defer func() {
		cancel()
		active.peer.Close()
		_ = conn.Close()
		s.mu.Lock()
		s.active, s.state = nil, State{}
		s.mu.Unlock()
		s.publish("state", s.State())
	}()
	conn.SetReadLimit(16 * 1024)
	if err := active.write(ctx, "subscribe", "subscribe", map[string]string{"eventName": "PlayerMessage"}); err != nil {
		return
	}
	go func() {
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
					err = &bridge.Error{Code: "INCOMPATIBLE", Message: "Add-on protocol must be version 1"}
				}
				s.mu.Lock()
				if s.active == active {
					s.state = State{Connected: true, Ready: err == nil}
					if err != nil {
						s.state.Error = err.Error()
					}
				}
				s.mu.Unlock()
				s.publish("state", s.State())
			}
		}
	}()
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
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
				err := &bridge.Error{Code: "COMMAND_REJECTED", Message: result.StatusMessage}
				if err.Message == "" {
					err.Message = "Minecraft rejected the command"
				}
				if id, ok := strings.CutPrefix(p.Header.RequestID, "request-"); ok {
					active.peer.Reject(id, err)
				}
				s.publish("error", err)
			}
		case "event":
			if raw := playerFrame(p); raw != nil {
				if err := active.peer.Receive(ctx, raw); err != nil {
					s.publish("error", bridge.Failure(err))
				}
			}
		}
	}
}

func playerFrame(p packet) []byte {
	var body struct {
		EventName string `json:"eventName"`
		Message   string `json:"message"`
		Sender    string `json:"sender"`
		Type      string `json:"type"`
	}
	if json.Unmarshal(p.Body, &body) != nil || (p.Header.EventName != "PlayerMessage" && body.EventName != "PlayerMessage") || body.Type != "say" || body.Sender != "Server" {
		return nil
	}
	message := strings.TrimPrefix(body.Message, "[Server] ")
	if raw, ok := strings.CutPrefix(message, Prefix); ok {
		return []byte(raw)
	}
	return nil
}

func (s *session) send(ctx context.Context, frame bridge.Frame) error {
	if err := ctx.Err(); err != nil {
		return &bridge.Error{Code: "TIMEOUT", Message: "Request deadline elapsed"}
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
		"commandLine": "scriptevent " + Namespace + ":message " + string(raw), "version": 1,
	})
}

func (s *session) write(ctx context.Context, purpose, id string, body any) error {
	select {
	case s.writer <- struct{}{}:
		defer func() { <-s.writer }()
	case <-ctx.Done():
		return &bridge.Error{Code: "TIMEOUT", Message: "Request deadline elapsed"}
	case <-s.ctx.Done():
		return &bridge.Error{Code: "DISCONNECTED", Message: "Minecraft disconnected"}
	}
	if s.ctx.Err() != nil {
		return &bridge.Error{Code: "DISCONNECTED", Message: "Minecraft disconnected"}
	}
	if ctx.Err() != nil {
		return &bridge.Error{Code: "TIMEOUT", Message: "Request deadline elapsed"}
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
		s.cancel()
		s.peer.Close()
		_ = s.conn.Close()
		return &bridge.Error{Code: "DISCONNECTED", Message: "Could not write to Minecraft"}
	}
	return nil
}

func (s *Server) publish(kind string, data any) {
	if s.Observe != nil {
		s.Observe(kind, data)
	}
}
