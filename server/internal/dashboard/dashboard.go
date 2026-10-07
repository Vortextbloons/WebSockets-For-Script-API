package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"scriptbridge/internal/bedrock"
	"scriptbridge/internal/bridge"
)

//go:embed static
var assets embed.FS

type Server struct {
	Game    *bedrock.Server
	App     *bridge.Bridge
	mu      sync.Mutex
	clients map[*websocket.Conn]chan []byte
}

func (s *Server) Publish(kind string, data any) {
	raw, err := json.Marshal(map[string]any{"type": kind, "data": data})
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn, queue := range s.clients {
		select {
		case queue <- raw:
		default:
			_ = conn.Close()
		}
	}
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/request", local(s.request))
	mux.HandleFunc("GET /api/live", local(s.live))
	files, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /", http.HandlerFunc(local(http.FileServer(http.FS(files)).ServeHTTP)))
}

func local(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		remote, _, remoteErr := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(remote)
		if err != nil || (host != "localhost" && host != "127.0.0.1") || remoteErr != nil || ip == nil || !ip.IsLoopback() {
			http.Error(w, "Local access only", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Scheme != "http" || parsed.Host != r.Host {
				http.Error(w, "Same origin required", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		next(w, r)
	}
}

func (s *Server) request(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var input struct {
		Target string          `json:"target"`
		Name   string          `json:"name"`
		Data   json.RawMessage `json:"data"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" || json.Unmarshal(raw, &input) != nil || (input.Target != "app" && input.Target != "game") || input.Name == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": &bridge.Error{Code: "INVALID_DATA", Message: "Expected {target: app|game, name, data} as JSON"}})
		return
	}
	if input.Data == nil {
		input.Data = json.RawMessage("null")
	}
	ctx, cancel := context.WithTimeout(r.Context(), bridge.Timeout)
	defer cancel()
	_, err = bridge.Encode(bridge.Frame{V: 1, Kind: "request", ID: "dashboard", Name: input.Name, Data: input.Data})
	var result any
	if err == nil {
		if input.Target == "app" {
			result, err = s.App.Call(ctx, input.Name, input.Data)
		} else {
			result, err = s.Game.Request(ctx, input.Name, input.Data)
		}
	}
	if err != nil {
		err := bridge.Failure(err)
		status := http.StatusBadGateway
		switch err.Code {
		case "INVALID_DATA", "PAYLOAD_TOO_LARGE":
			status = http.StatusBadRequest
		case "UNKNOWN_REQUEST":
			status = http.StatusNotFound
		case "BUSY":
			status = http.StatusTooManyRequests
		case "NOT_READY", "DISCONNECTED":
			status = http.StatusServiceUnavailable
		case "TIMEOUT":
			status = http.StatusGatewayTimeout
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": result})
}

func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	queue := make(chan []byte, 32)
	s.mu.Lock()
	if s.clients == nil {
		s.clients = make(map[*websocket.Conn]chan []byte)
	}
	initial, _ := json.Marshal(map[string]any{"type": "state", "data": s.Game.State()})
	queue <- initial
	s.clients[conn] = queue
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, conn)
		s.mu.Unlock()
		_ = conn.Close()
	}()
	done := make(chan struct{})
	conn.SetReadLimit(2048)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(60 * time.Second)) })
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case raw := <-queue:
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if conn.WriteMessage(websocket.TextMessage, raw) != nil {
				return
			}
		case <-ticker.C:
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		}
	}
}
