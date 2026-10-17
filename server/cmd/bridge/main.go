package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"scriptbridge-starter/internal/dashboard"
	"scriptbridge-starter/internal/features"
	"scriptbridge-starter/internal/public"
	"scriptbridge/bedrock"
	"scriptbridge/bridge"
)

func main() {
	publicDir := flag.String("public-dir", "", "Website directory; empty disables the public server")
	publicActions := flag.String("public-actions", "", "Comma-separated Minecraft requests allowed through the public API")
	flag.Parse()
	if *publicActions != "" && (*publicDir == "" || os.Getenv("BRIDGE_PUBLIC_TOKEN") == "") {
		log.Fatal("Public actions require -public-dir and BRIDGE_PUBLIC_TOKEN")
	}
	if *publicDir != "" {
		info, err := os.Stat(*publicDir)
		if err != nil || !info.IsDir() {
			log.Fatal("public-dir must be an existing website directory")
		}
	}
	app := bridge.New(nil, bridge.Options{})
	features.Register(app)
	game, err := bedrock.New(bedrock.Config{Namespace: "scaffold", Prefix: "§8[SC] "})
	if err != nil {
		log.Fatal(err)
	}
	game.Register = features.Register
	dash := &dashboard.Server{Game: game, App: app}
	game.Observe = dash.Publish
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", game.Handler)
	dash.Register(mux)
	server := &http.Server{Addr: "127.0.0.1:8765", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	servers := []*http.Server{server}
	if *publicDir != "" {
		servers = append(servers, &http.Server{
			Addr:              "127.0.0.1:8780",
			Handler:           public.Handler(http.Dir(*publicDir), os.Getenv("BRIDGE_PUBLIC_TOKEN"), strings.Split(*publicActions, ","), game.Request),
			ReadHeaderTimeout: 5 * time.Second,
		})
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		game.Close()
		deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, srv := range servers {
			_ = srv.Shutdown(deadline)
		}
	}()
	log.Print("Dashboard: http://127.0.0.1:8765")
	log.Print("In Minecraft: /wsserver localhost:8765/ws")
	results := make(chan error, len(servers))
	for _, srv := range servers {
		go func(srv *http.Server) { results <- srv.ListenAndServe() }(srv)
	}
	if *publicDir != "" {
		log.Print("Public website: http://127.0.0.1:8780")
	}
	for range servers {
		if err := <-results; err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}
}
