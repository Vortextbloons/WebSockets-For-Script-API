# Go bridge

Public packages: `scriptbridge/bridge` for JSON routing and `scriptbridge/bedrock` for Minecraft's WebSocket transport. No dashboard or feature examples are required.

```go
game, err := bedrock.New(bedrock.Config{Namespace: "myaddon", Prefix: "[MY] "})
if err != nil {
    log.Fatal(err)
}
game.Register = func(b *bridge.Bridge) {
    bridge.HandleTyped(b, "app.echo", func(_ context.Context, req echoRequest) (echoReply, error) {
        return echoReply{Text: *req.Text}, nil
    })
}
defer game.Close()
mux := http.NewServeMux()
mux.HandleFunc("GET /ws", game.Handler)
```

Serve the mux on a loopback address. Match the add-on's namespace and prefix. `Register` runs for each world connection; set callbacks before serving.

## Typed requests and events

`HandleTyped[Req, Res]`, `RequestTyped[Res]`, and `OnTyped[T]` decode JSON into your Go types. A payload that does not decode, or fails validation, returns `INVALID_DATA` and never reaches your code. Implement `Validate() error` on a type to add schema rules; the error message is sent to the peer.

```go
type echoRequest struct {
    Text *string `json:"text"`
}

func (r echoRequest) Validate() error {
    if r.Text == nil {
        return errors.New("Expected {text: string}")
    }
    return nil
}
```

Use `bridge.Parse[T](raw)` to decode raw JSON the same way. Unknown fields are ignored, so newer peers can add fields.

## Errors

Return `*bridge.Error` to choose a code. The standard codes are exported as `bridge.CodeInvalidData`, `bridge.CodeUnknownRequest`, `bridge.CodeTimeout`, and so on; `bridge.Errorf(code, format, args...)` builds one. Other errors become `HANDLER_ERROR`. The TypeScript package uses the same codes.

## Connection state and health

`State()` returns a snapshot:

- `Connected`, `Ready`, `Error`, and `ConnectedAt` describe the session and add-on handshake.
- `Health` has `LastSeen`, `LatencyMs`, and `MissedHeartbeats`.
- `Disconnect` is the last session's end: `Reason`, `Code` (WebSocket close code, when sent), `Message`, `Guidance`, and `At`.

Reasons: `MINECRAFT_CLOSED`, `CONNECTION_LOST`, `MESSAGE_TOO_LARGE`, `WRITE_FAILED`, `HEARTBEAT_TIMEOUT`, and `SERVER_STOPPED`.

The server sends `bridge.ping` every `Config.Heartbeat` (default 30 seconds; negative disables). Any reply counts as alive, including `UNKNOWN_REQUEST` from older add-ons. Three missed heartbeats in a row end the session as `HEARTBEAT_TIMEOUT`. Each ping prints one chat line in Minecraft.

## Panics

A panic in an event listener is recovered, reported through `bridge.Options.OnError`, and the other listeners keep running. Without `OnError`, reports go to the standard logger. Background goroutines started by `bedrock` are protected the same way.

## Other transports

For another transport, use `bridge.New(sender, bridge.Options{OnError: ...})` and feed encoded incoming frames to `Receive`. `Handle` registers requests; `On` returns an unsubscribe function. `Close` rejects pending requests and clears registered handlers and listeners. Handlers should honor their context. Senders and observer callbacks can run concurrently; serialize writes as your transport requires.

`bridge.Options.Timeout` and `bedrock.Config.Timeout` default to 10 seconds; callers can set shorter context deadlines. Frames are limited to 2,048 bytes, with 16 pending requests and 16 active handlers.

Until a repository import path is chosen, an independent app can use:

```go
require scriptbridge v0.1.0
replace scriptbridge => /absolute/path/to/packages/go
```

Replace the module path and imports with the public repository path before a tagged release, for example `github.com/your-org/your-repo/packages/go`. Use tags such as `packages/go/v0.1.0` ([Go release guidance](https://go.dev/doc/modules/managing-source)). The starter's `server/go.mod` demonstrates local consumption.
