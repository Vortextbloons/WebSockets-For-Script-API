package bedrock

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func connect(t *testing.T) (*Server, *websocket.Conn, string) {
	t.Helper()
	states := make(chan State, 16)
	game, err := New(Config{Namespace: "custom", Prefix: testPrefix})
	if err != nil {
		t.Fatal(err)
	}
	game.Register = func(b *bridge.Bridge) {
		b.Handle("app.echo", func(_ context.Context, data json.RawMessage) (any, error) { return data, nil })
	}
	game.Observe = func(kind string, data any) {
		if kind == "state" {
			states <- data.(State)
		}
	}
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
	hello, id := readFrame(t, conn)
	if hello.Name != "bridge.hello" {
		t.Fatal(hello)
	}
	if err := conn.WriteJSON(map[string]any{"header": map[string]string{"messagePurpose": "commandResponse", "requestId": id}, "body": map[string]int{"statusCode": 0}}); err != nil {
		t.Fatal(err)
	}
	if game.State().Ready {
		t.Fatal("command acknowledgement incorrectly completed handshake")
	}
	sendFrame(t, conn, bridge.Frame{V: 1, Kind: "response", ID: hello.ID, Data: json.RawMessage(`{"protocol":1}`)})
	for {
		select {
		case state := <-states:
			if state.Ready {
				return game, conn, server.URL
			}
		case <-time.After(time.Second):
			t.Fatal("handshake did not complete")
		}
	}
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
	if err := <-done; bridge.Failure(err).Code != "COMMAND_REJECTED" {
		t.Fatal(err)
	}
	go func() { _, err := game.Request(context.Background(), "pending", nil); done <- err }()
	readFrame(t, conn)
	_ = conn.Close()
	select {
	case err := <-done:
		if bridge.Failure(err).Code != "DISCONNECTED" {
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
