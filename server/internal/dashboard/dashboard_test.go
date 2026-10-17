package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"scriptbridge-starter/internal/features"
	"scriptbridge/bedrock"
	"scriptbridge/bridge"
)

func TestRequestAPIAndOriginProtection(t *testing.T) {
	app := bridge.New(nil, bridge.Options{})
	features.Register(app)
	dash := &Server{Game: &bedrock.Server{}, App: app}
	mux := http.NewServeMux()
	dash.Register(mux)
	for _, tc := range []struct {
		body, origin, contentType, host string
		status                          int
	}{
		{`{"target":"app","name":"app.echo","data":{"text":"<script>alert(1)</script>"}}`, "http://127.0.0.1:8765", "application/json", "127.0.0.1:8765", 200},
		{`{"target":"game","name":"game.time","data":null}`, "", "application/json", "127.0.0.1:8765", 503},
		{`{"target":"app","name":"app.echo","data":null}`, "", "application/json", "127.0.0.1:8765", 400},
		{`{"target":"app","name":"missing","data":null}`, "", "application/json", "127.0.0.1:8765", 404},
		{`{}`, "http://malicious.example", "application/json", "127.0.0.1:8765", 403},
		{`{}`, "", "text/plain", "127.0.0.1:8765", 400},
		{`{}`, "", "application/json", "malicious.example:8765", 403},
		{`{"target":"app","name":"app.echo","data":{"text":"` + strings.Repeat("x", 3000) + `"}}`, "", "application/json", "127.0.0.1:8765", 400},
	} {
		r := httptest.NewRequest(http.MethodPost, "http://"+tc.host+"/api/request", strings.NewReader(tc.body))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", tc.contentType)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
		}
		if w.Code == 200 {
			var result struct {
				Data struct {
					Text string `json:"text"`
				} `json:"data"`
			}
			if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Data.Text != "<script>alert(1)</script>" {
				t.Fatal(w.Body.String())
			}
		}
	}
}

func TestLiveStateTrafficAndBrowserOrigin(t *testing.T) {
	dash := &Server{Game: &bedrock.Server{}}
	mux := http.NewServeMux()
	dash.Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	address := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/live"
	conn, _, err := websocket.DefaultDialer.Dial(address, http.Header{"Origin": []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var message struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := conn.ReadJSON(&message); err != nil || message.Type != "state" {
		t.Fatal(err, message)
	}
	dash.Publish("frame", map[string]any{"direction": "in", "frame": map[string]any{"name": "game.player_joined"}})
	if err := conn.ReadJSON(&message); err != nil || message.Type != "frame" {
		t.Fatal(err, message)
	}
	other, response, err := websocket.DefaultDialer.Dial(address, http.Header{"Origin": []string{"http://malicious.example"}})
	if other != nil {
		_ = other.Close()
	}
	if err == nil || response.StatusCode != 403 {
		t.Fatal("accepted cross-origin browser")
	}
}

func TestResponsesIncludeDurationAndLiveMessagesIncludeTime(t *testing.T) {
	app := bridge.New(nil, bridge.Options{})
	features.Register(app)
	dash := &Server{Game: &bedrock.Server{}, App: app}
	mux := http.NewServeMux()
	dash.Register(mux)

	post := func(body string) map[string]json.RawMessage {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/api/request", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var out map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err, w.Body.String())
		}
		return out
	}
	ok := post(`{"target":"app","name":"app.echo","data":{"text":"hi"}}`)
	if _, found := ok["durationMs"]; !found || string(ok["data"]) != `{"text":"hi"}` {
		t.Fatalf("success response missing duration: %v", ok)
	}
	failed := post(`{"target":"app","name":"app.echo","data":{}}`)
	var failure struct {
		Code string `json:"code"`
	}
	if _, found := failed["durationMs"]; !found || json.Unmarshal(failed["error"], &failure) != nil || failure.Code != bridge.CodeInvalidData {
		t.Fatalf("validation failure not standardized: %v", failed)
	}

	server := httptest.NewServer(mux)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/live", http.Header{"Origin": []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var message struct {
		Type string `json:"type"`
		At   int64  `json:"at"`
	}
	if err := conn.ReadJSON(&message); err != nil || message.Type != "state" || message.At == 0 {
		t.Fatal(err, message)
	}
	dash.Publish("error", &bridge.Error{Code: bridge.CodeListenerError, Message: "boom"})
	if err := conn.ReadJSON(&message); err != nil || message.Type != "error" || message.At == 0 {
		t.Fatal(err, message)
	}
}

func TestStatusMappingCoversStandardCodes(t *testing.T) {
	for code, want := range map[string]int{
		bridge.CodeInvalidData: 400, bridge.CodePayloadTooLarge: 400, bridge.CodeUnknownRequest: 404,
		bridge.CodeBusy: 429, bridge.CodeNotReady: 503, bridge.CodeDisconnected: 503,
		bridge.CodeTimeout: 504, bridge.CodeHandlerError: 502, bridge.CodeCommandRejected: 502,
	} {
		if got := statusFor(code); got != want {
			t.Fatalf("%s -> %d, want %d", code, got, want)
		}
	}
}
