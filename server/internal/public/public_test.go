package public

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"scriptbridge/bedrock"
)

func TestWebsiteAndMinecraftAccess(t *testing.T) {
	files := http.FS(fstest.MapFS{"index.html": {Data: []byte("website")}})
	game := &bedrock.Server{}
	for _, tc := range []struct {
		path, token, body string
		status            int
	}{
		{"/", "", "", 200},
		{"/api/request", "secret", `{}`, 404},
		{"/api/live", "secret", `{}`, 404},
		{"/api/minecraft", "", `{"name":"game.time"}`, 401},
		{"/api/minecraft", "wrong", `{"name":"game.time"}`, 401},
		{"/api/minecraft", "secret", `{"name":"game.command"}`, 403},
		{"/api/minecraft", "secret", `{`, 400},
		{"/api/minecraft", "secret", `{"name":"game.time","data":null}`, 503},
	} {
		handler := Handler(files, "secret", []string{"game.time"}, game.Request)
		method := http.MethodPost
		if tc.path == "/" {
			method = http.MethodGet
		}
		r := httptest.NewRequest(method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: got %d, want %d: %s", tc.path, w.Code, tc.status, w.Body.String())
		}
	}
	for _, token := range []string{"", "secret"} {
		handler := Handler(files, token, nil, game.Request)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/minecraft", nil))
		if w.Code != 404 {
			t.Fatalf("disabled API: got %d", w.Code)
		}
	}
}

func TestAllowedActionAndRateLimit(t *testing.T) {
	calls := 0
	handler := Handler(http.FS(fstest.MapFS{}), "secret", []string{"game.time"}, func(_ context.Context, name string, data any) (json.RawMessage, error) {
		calls++
		if name != "game.time" || string(data.(json.RawMessage)) != "null" {
			t.Fatal("unexpected request")
		}
		return json.RawMessage(`{"ticks":1200}`), nil
	})
	for i := 0; i < 21; i++ {
		r := httptest.NewRequest(http.MethodPost, "/api/minecraft", strings.NewReader(`{"name":"game.time"}`))
		r.Header.Set("Authorization", "Bearer secret")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		want := 200
		if i == 20 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("got %d, want %d", w.Code, want)
		}
		if i == 0 && !strings.Contains(w.Body.String(), `"ticks":1200`) {
			t.Fatal(w.Body.String())
		}
	}
	if calls != 20 {
		t.Fatalf("got %d calls", calls)
	}
}
