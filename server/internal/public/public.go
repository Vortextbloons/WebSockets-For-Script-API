package public

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"scriptbridge/bridge"
)

func Handler(files http.FileSystem, token string, actions []string, request func(context.Context, string, any) (json.RawMessage, error)) http.Handler {
	mux := http.NewServeMux()
	allowed := make(map[string]bool)
	for _, name := range actions {
		if name = strings.TrimSpace(name); name != "" {
			allowed[name] = true
		}
	}
	var mu sync.Mutex
	var window time.Time
	count := 0
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	if token != "" && len(allowed) > 0 {
		mux.HandleFunc("POST /api/minecraft", func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			mu.Lock()
			if time.Since(window) >= time.Second {
				window, count = time.Now(), 0
			}
			limited := count >= 20
			if !limited {
				count++
			}
			mu.Unlock()
			if limited {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "Too many requests", http.StatusTooManyRequests)
				return
			}
			var input struct {
				Name string          `json:"name"`
				Data json.RawMessage `json:"data"`
			}
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2048))
			if err != nil || strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" || json.Unmarshal(raw, &input) != nil {
				http.Error(w, "Expected {name, data} as JSON", http.StatusBadRequest)
				return
			}
			if !allowed[input.Name] {
				http.Error(w, "Action not allowed", http.StatusForbidden)
				return
			}
			if input.Data == nil {
				input.Data = json.RawMessage("null")
			}
			if _, err := bridge.Encode(bridge.Frame{V: 1, Kind: "request", ID: "public", Name: input.Name, Data: input.Data}); err != nil {
				http.Error(w, "Invalid request", http.StatusBadRequest)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), bridge.Timeout)
			defer cancel()
			result, err := request(ctx, input.Name, input.Data)
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				failure := bridge.Failure(err)
				status := http.StatusBadGateway
				switch failure.Code {
				case bridge.CodeNotReady, bridge.CodeDisconnected:
					status = http.StatusServiceUnavailable
				case bridge.CodeInvalidData, bridge.CodePayloadTooLarge:
					status = http.StatusBadRequest
				case bridge.CodeBusy:
					status = http.StatusTooManyRequests
				case bridge.CodeTimeout:
					status = http.StatusGatewayTimeout
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": failure})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": result})
		})
	}
	mux.Handle("/", http.FileServer(files))
	return mux
}
