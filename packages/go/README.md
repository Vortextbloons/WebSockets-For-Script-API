# Go bridge

Public packages: `scriptbridge/bridge` for JSON routing and `scriptbridge/bedrock` for Minecraft's WebSocket transport. No dashboard or feature examples are required.

```go
game, err := bedrock.New(bedrock.Config{Namespace: "myaddon", Prefix: "[MY] "})
if err != nil {
    log.Fatal(err)
}
game.Register = func(b *bridge.Bridge) {
    b.Handle("app.echo", func(_ context.Context, data json.RawMessage) (any, error) {
        return data, nil
    })
}
defer game.Close()
mux := http.NewServeMux()
mux.HandleFunc("GET /ws", game.Handler)
```

Serve the mux on a loopback address. Match the add-on's namespace and prefix. `Register` runs for each world connection; set callbacks before serving. `State().Ready` indicates the add-on handshake completed. Use `game.Request(ctx, name, data)` and `game.Emit(ctx, name, data)` to reach the add-on.

For another transport, use `bridge.New(sender, bridge.Options{})` and feed encoded incoming frames to `Receive`. `Handle` registers requests; `On` returns an unsubscribe function. `Close` rejects pending requests and clears registered handlers and listeners. Handlers should honor their context. Senders and observer callbacks can run concurrently; serialize writes as your transport requires.

`bridge.Options.Timeout` and `bedrock.Config.Timeout` default to 10 seconds; callers can set shorter context deadlines. Frames are limited to 2,048 bytes, with 16 pending requests and 16 active handlers.

Until a repository import path is chosen, an independent app can use:

```go
require scriptbridge v0.1.0
replace scriptbridge => /absolute/path/to/packages/go
```

Replace the module path and imports with the public repository path before a tagged release, for example `github.com/your-org/your-repo/packages/go`. Use tags such as `packages/go/v0.1.0` ([Go release guidance](https://go.dev/doc/modules/managing-source)). The starter's `server/go.mod` demonstrates local consumption.
