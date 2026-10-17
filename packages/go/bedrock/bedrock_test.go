package bedrock

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"scriptbridge/bridge"
)

const testPrefix = "§8[CUSTOM] "

func readFrame(t *testing.T, conn *websocket.Conn) (bridge.Frame, string) {
	t.Helper()
	var p packet
	if err := conn.ReadJSON(&p); err != nil {
		t.Fatal(err)
	}
	var body struct {
		CommandLine string `json:"commandLine"`
	}
	if err := json.Unmarshal(p.Body, &body); err != nil {
		t.Fatal(err)
	}
	raw, ok := strings.CutPrefix(body.CommandLine, "scriptevent custom:message ")
	if !ok {
		t.Fatal(body.CommandLine)
	}
	frame, err := bridge.Decode([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return frame, p.Header.RequestID
}

func sendFrame(t *testing.T, conn *websocket.Conn, frame bridge.Frame) {
	t.Helper()
	raw, err := bridge.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(map[string]any{
		"header": map[string]any{"messagePurpose": "event", "eventName": "PlayerMessage"},
		"body":   map[string]string{"message": "[Server] " + testPrefix + string(raw), "sender": "Server", "type": "say"},
	}); err != nil {
		t.Fatal(err)
	}
}

// observed collects published state and errors without blocking the server.
type observed struct {
	states chan State
	errors chan *bridge.Error
}

func (o *observed) observe(kind string, data any) {
	switch value := data.(type) {
	case State:
		if kind == "state" {
			select {
			case o.states <- value:
			default:
			}
		}
	case *bridge.Error:
		if kind == "error" {
			select {
			case o.errors <- value:
			default:
			}
		}
	}
}

func echoRegister(b *bridge.Bridge) {
	b.Handle("app.echo", func(_ context.Context, data json.RawMessage) (any, error) { return data, nil })
}

// connectWith starts a Minecraft session and completes the add-on handshake.
// Heartbeat frames can arrive before the hello, so the helper skips anything that is not hello.
func connectWith(t *testing.T, config Config, register func(*bridge.Bridge)) (*Server, *websocket.Conn, string, *observed) {
	t.Helper()
	seen := &observed{states: make(chan State, 512), errors: make(chan *bridge.Error, 512)}
	if config.Namespace == "" {
		config.Namespace = "custom"
	}
	if config.Prefix == "" {
		config.Prefix = testPrefix
	}
	game, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	game.Register = register
	game.Observe = seen.observe
	server := httptest.NewServer(http.HandlerFunc(game.Handler))
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); game.Close(); server.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var subscription packet
	if err := conn.ReadJSON(&subscription); err != nil {
		t.Fatal(err)
	}
	if subscription.Header.MessagePurpose != "subscribe" {
		t.Fatal(subscription)
	}
	for {
		hello, id := readFrame(t, conn)
		if hello.Name != "bridge.hello" {
			continue
		}
		if err := conn.WriteJSON(map[string]any{"header": map[string]string{"messagePurpose": "commandResponse", "requestId": id}, "body": map[string]int{"statusCode": 0}}); err != nil {
			t.Fatal(err)
		}
		if game.State().Ready {
			t.Fatal("command acknowledgement incorrectly completed handshake")
		}
		sendFrame(t, conn, bridge.Frame{V: 1, Kind: "response", ID: hello.ID, Data: json.RawMessage(`{"protocol":1}`)})
		break
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case state := <-seen.states:
			if state.Ready {
				_ = conn.SetReadDeadline(time.Time{})
				return game, conn, server.URL, seen
			}
		case <-deadline:
			t.Fatal("handshake did not complete")
		}
	}
}

func connect(t *testing.T) (*Server, *websocket.Conn, string) {
	t.Helper()
	game, conn, address, _ := connectWith(t, Config{}, echoRegister)
	return game, conn, address
}

// serveAddon plays the add-on: it reads every command the server sends and answers when reply says to.
func serveAddon(conn *websocket.Conn, reply func(bridge.Frame) (bridge.Frame, bool)) {
	go func() {
		for {
			var p packet
			if err := conn.ReadJSON(&p); err != nil {
				return
			}
			var body struct {
				CommandLine string `json:"commandLine"`
			}
			if json.Unmarshal(p.Body, &body) != nil {
				continue
			}
			raw, ok := strings.CutPrefix(body.CommandLine, "scriptevent custom:message ")
			if !ok {
				continue
			}
			frame, err := bridge.Decode([]byte(raw))
			if err != nil {
				continue
			}
			if response, ok := reply(frame); ok {
				raw, err := bridge.Encode(response)
				if err != nil {
					continue
				}
				_ = conn.WriteJSON(map[string]any{
					"header": map[string]any{"messagePurpose": "event", "eventName": "PlayerMessage"},
					"body":   map[string]string{"message": "[Server] " + testPrefix + string(raw), "sender": "Server", "type": "say"},
				})
			}
		}
	}()
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitDisconnect(t *testing.T, game *Server, reason string) Disconnect {
	t.Helper()
	var found *Disconnect
	waitUntil(t, func() bool {
		found = game.State().Disconnect
		return found != nil
	})
	if found.Reason != reason {
		t.Fatalf("disconnect reason %s, want %s (%+v)", found.Reason, reason, found)
	}
	return *found
}

func TestBidirectionalRoundTrip(t *testing.T) {
	game, conn, _ := connect(t)
	done := make(chan json.RawMessage, 1)
	go func() {
		result, err := game.Request(context.Background(), "game.time", nil)
		if err != nil {
			done <- json.RawMessage(`"failed"`)
			return
		}
		done <- result
	}()
	request, _ := readFrame(t, conn)
	sendFrame(t, conn, bridge.Frame{V: 1, Kind: "response", ID: request.ID, Data: json.RawMessage(`{"ticks":1234}`)})
	if result := <-done; string(result) != `{"ticks":1234}` {
		t.Fatal(string(result))
	}
	raw := json.RawMessage(`{"text":"Quotes \"slashes\\\"\nline 😀"}`)
	sendFrame(t, conn, bridge.Frame{V: 1, Kind: "request", ID: "addon-example", Name: "app.echo", Data: raw})
	response, _ := readFrame(t, conn)
	if response.Kind != "response" || response.ID != "addon-example" || string(response.Data) != string(raw) {
		t.Fatalf("unexpected echo: %+v", response)
	}
	if err := game.Emit(context.Background(), "app.notice", nil); err != nil {
		t.Fatal(err)
	}
	event, _ := readFrame(t, conn)
	if event.Kind != "event" || event.Name != "app.notice" {
		t.Fatal(event)
	}
}

func TestCommandRejectionDisconnectAndSecondConnection(t *testing.T) {
	game, conn, address := connect(t)
	other, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(address, "http"), nil)
	if other != nil {
		_ = other.Close()
	}
	if err == nil || response.StatusCode != http.StatusConflict {
		t.Fatal("second world was accepted")
	}
	done := make(chan error, 1)
	go func() { _, err := game.Request(context.Background(), "bad", nil); done <- err }()
	_, id := readFrame(t, conn)
	if err := conn.WriteJSON(map[string]any{
		"header": map[string]string{"messagePurpose": "commandResponse", "requestId": id},
		"body":   map[string]any{"statusCode": -1, "statusMessage": "Script event unavailable"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; bridge.Failure(err).Code != bridge.CodeCommandRejected {
		t.Fatal(err)
	}
	go func() { _, err := game.Request(context.Background(), "pending", nil); done <- err }()
	readFrame(t, conn)
	_ = conn.Close()
	select {
	case err := <-done:
		if bridge.Failure(err).Code != bridge.CodeDisconnected {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect did not fail pending request")
	}
}

func TestPlayerMessageFiltering(t *testing.T) {
	frame := `{"v":1,"kind":"event","name":"changed","data":null}`
	for _, tc := range []struct {
		text, sender, kind string
		accepted           bool
	}{
		{testPrefix + frame, "Server", "say", true},
		{"[Server] " + testPrefix + frame, "Server", "say", true},
		{testPrefix + frame, "Steve", "say", false},
		{testPrefix + frame, "Server", "chat", false},
		{"unrelated " + testPrefix + frame, "Server", "say", false},
	} {
		body, _ := json.Marshal(map[string]string{"message": tc.text, "sender": tc.sender, "type": tc.kind})
		p := packet{Body: body}
		p.Header.EventName = "PlayerMessage"
		if got := playerFrame(p, testPrefix) != nil; got != tc.accepted {
			t.Fatalf("wrong filter result: %+v", tc)
		}
	}
}

func TestAddOnRestartRehandshakes(t *testing.T) {
	game, conn, _ := connect(t)
	sendFrame(t, conn, bridge.Frame{V: 1, Kind: "event", Name: "bridge.started", Data: json.RawMessage("null")})
	frame, _ := readFrame(t, conn)
	if frame.Name != "bridge.hello" {
		t.Fatal(frame)
	}
	sendFrame(t, conn, bridge.Frame{V: 1, Kind: "response", ID: frame.ID, Data: json.RawMessage(`{"protocol":1}`)})
	game.Close()
}

func TestNotReadyExplainsWhy(t *testing.T) {
	game, err := New(Config{Namespace: "custom", Prefix: testPrefix})
	if err != nil {
		t.Fatal(err)
	}
	_, err = game.Request(context.Background(), "game.time", nil)
	if bridge.Failure(err).Code != bridge.CodeNotReady || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestMinecraftCloseIsExplained(t *testing.T) {
	game, conn, _, _ := connectWith(t, Config{Heartbeat: -1}, echoRegister)
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "world exited"), time.Now().Add(time.Second))
	d := waitDisconnect(t, game, ReasonMinecraftClosed)
	if d.Code != websocket.CloseNormalClosure || !strings.Contains(d.Message, "world exited") || d.Guidance == "" || d.At == 0 {
		t.Fatalf("incomplete diagnostics: %+v", d)
	}
	if game.State().Connected {
		t.Fatal("state still reports a connection")
	}
}

func TestServerCloseIsExplained(t *testing.T) {
	game, _, _, _ := connectWith(t, Config{Heartbeat: -1}, echoRegister)
	game.Close()
	waitDisconnect(t, game, ReasonServerStopped)
}

func TestOversizedMessageIsExplained(t *testing.T) {
	game, conn, _, _ := connectWith(t, Config{Heartbeat: -1}, echoRegister)
	_ = conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 20*1024)))
	waitDisconnect(t, game, ReasonMessageTooLarge)
}

func TestHeartbeatKeepsLiveAddOnHealthy(t *testing.T) {
	// "current" answers bridge.ping; "old" is an add-on from before heartbeats, which answers UNKNOWN_REQUEST.
	for _, kind := range []string{"current", "old"} {
		t.Run(kind, func(t *testing.T) {
			game, conn, _, _ := connectWith(t, Config{Heartbeat: 5 * time.Millisecond, Timeout: time.Second}, echoRegister)
			var answered atomic.Int32
			serveAddon(conn, func(frame bridge.Frame) (bridge.Frame, bool) {
				if frame.Name != "bridge.ping" {
					return bridge.Frame{}, false
				}
				answered.Add(1)
				if kind == "old" {
					return bridge.Frame{V: 1, Kind: "response", ID: frame.ID, Error: &bridge.Error{Code: bridge.CodeUnknownRequest, Message: "Unknown request: bridge.ping"}}, true
				}
				return bridge.Frame{V: 1, Kind: "response", ID: frame.ID, Data: json.RawMessage("null")}, true
			})
			waitUntil(t, func() bool { return answered.Load() >= 3 })
			state := game.State()
			if !state.Connected || !state.Ready || state.Disconnect != nil || state.Health.MissedHeartbeats != 0 || state.Health.LastSeen == 0 {
				t.Fatalf("healthy add-on reported as unhealthy: %+v", state)
			}
		})
	}
}

func TestSilentAddOnIsDisconnected(t *testing.T) {
	game, conn, _, _ := connectWith(t, Config{Heartbeat: 5 * time.Millisecond, Timeout: 20 * time.Millisecond}, echoRegister)
	serveAddon(conn, func(bridge.Frame) (bridge.Frame, bool) { return bridge.Frame{}, false })
	d := waitDisconnect(t, game, ReasonHeartbeatTimeout)
	if game.State().Connected || !strings.Contains(d.Message, "missed") || d.Guidance == "" {
		t.Fatalf("silent add-on not explained: %+v", d)
	}
	if _, err := game.Request(context.Background(), "game.time", nil); bridge.Failure(err).Code != bridge.CodeNotReady {
		t.Fatalf("request after heartbeat timeout: %v", err)
	}
}

func TestPanickingListenerDoesNotStopTraffic(t *testing.T) {
	register := func(b *bridge.Bridge) {
		echoRegister(b)
		b.On("app.broken", func(json.RawMessage) { panic("listener bug") })
	}
	game, conn, _, seen := connectWith(t, Config{Heartbeat: -1}, register)
	sendFrame(t, conn, bridge.Frame{V: 1, Kind: "event", Name: "app.broken", Data: json.RawMessage("null")})
	select {
	case err := <-seen.errors:
		if err.Code != bridge.CodeListenerError || !strings.Contains(err.Message, "listener bug") {
			t.Fatalf("unexpected report: %+v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("listener panic was not reported")
	}
	done := make(chan json.RawMessage, 1)
	go func() {
		result, err := game.Request(context.Background(), "game.time", nil)
		if err != nil {
			done <- json.RawMessage(`"failed"`)
			return
		}
		done <- result
	}()
	request, _ := readFrame(t, conn)
	sendFrame(t, conn, bridge.Frame{V: 1, Kind: "response", ID: request.ID, Data: json.RawMessage(`{"ticks":7}`)})
	if result := <-done; string(result) != `{"ticks":7}` {
		t.Fatalf("connection stopped working after a listener panic: %s", result)
	}
	if !game.State().Connected {
		t.Fatal("listener panic disconnected Minecraft")
	}
}
