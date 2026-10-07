package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"scriptbridge/internal/bedrock"
	"scriptbridge/internal/bridge"
	"scriptbridge/internal/dashboard"
	"scriptbridge/internal/features"
)

func main() {
	app := bridge.New(nil, nil)
	features.Register(app)
	game := &bedrock.Server{Register: features.Register}
	dash := &dashboard.Server{Game: game, App: app}
	game.Observe = dash.Publish
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", game.Handler)
	dash.Register(mux)
	server := &http.Server{Addr: "127.0.0.1:8765", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		game.Close()
		deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(deadline)
	}()
	log.Print("Dashboard: http://127.0.0.1:8765")
	log.Print("In Minecraft: /wsserver localhost:8765/ws")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
