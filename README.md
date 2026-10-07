# Script API Bridge

A copyable Minecraft Bedrock + Go starter for small JSON requests and events. Generated with the Script API Helper MCP, then reduced to a behavior pack, bridge, examples, and a local dashboard.

## Run

Requires Node.js 22+, Go 1.22+, and Minecraft for Windows with `/wsserver` support. The add-on pins stable `@minecraft/server` 2.9.0 and targets Minecraft 1.26.50+. Beta APIs and a resource pack are not required.

```powershell
npm install
npm run typecheck
npm run build
npm run server
```

Copy `addon/behavior_pack` into your Minecraft installation's `development_behavior_packs` folder. Activate **Script API Bridge** in a world with cheats enabled, then enter:

```text
/wsserver localhost:8765/ws
```

Open <http://127.0.0.1:8765>. The dashboard shows **Minecraft ready** after the add-on replies to the handshake. Its Local app example works without Minecraft; select Minecraft to request `game.time` with `null` data.

Test Minecraft → app inside the world:

```text
/scriptevent scaffold:demo Hello from Minecraft
```

The app returns the text and the add-on displays it. Initial player spawns publish `game.player_joined` events. Only events occurring after connection appear in the dashboard.

If Minecraft requests encrypted WebSockets, turn off **Encrypted Websockets** in Minecraft settings; this scaffold uses a local unencrypted connection. Reconnect manually after restarting the app. Reloading the add-on announces startup and triggers a new handshake if the game socket is still connected.

## How it works

```text
Dashboard ── HTTP requests / live WebSocket ── Go app
                                               ↕ Minecraft WebSocket
                                            Minecraft
                                               ↕ commands / script events
                                          TypeScript add-on
```

Minecraft opens the socket, not the add-on. The app subscribes to `PlayerMessage` events. App → add-on messages use `scriptevent scaffold:message <json>` in Minecraft command packets. Add-on → app messages use `say §8[SC] <json>` and arrive as server `say` events. The bridge only accepts the expected server message shape and prefix; a player's chat message containing the prefix is ignored.

Outbound add-on frames are visible in chat. Do not put secrets in them. This transport is for small messages in a trusted local world. The namespace, prefix, and ScriptEventSource filtering are routing guards, not authentication against privileged operators or other behavior packs.

The game socket (`/ws`) and browser socket (`/api/live`) are separate protocols. A successful command response means Minecraft accepted a command; only the bridge response means a request handler completed. Command rejections fail the matching pending request.

## Add a feature

Add-on handlers live in `addon/src/features.ts`:

```typescript
bridge.handle("game.players", () =>
  world.getAllPlayers().map((player) => player.name)
);

const result = await bridge.request("app.echo", { text: "Hello" });
bridge.emit("game.changed", { value: 3 });
const unsubscribe = bridge.on("app.notice", (data) => world.sendMessage(JSON.stringify(data)));
```

Go handlers live in `server/internal/features/features.go`:

```go
b.Handle("app.example", func(ctx context.Context, raw json.RawMessage) (any, error) {
    return map[string]string{"message": "Hello"}, nil
})
```

Use `b.Request(ctx, name, data)`, `b.Emit(ctx, name, data)`, and `b.On(name, listener)` inside handlers registered with the connected bridge. At application startup, `game.Request` and `game.Emit` address the connected add-on. `On` returns an unsubscribe function. Event listeners should do short, synchronous work; use request handlers for asynchronous processing. Go handlers should honor their context.

Handlers validate their data. Return `bridge.Error{Code: "INVALID_DATA", Message: "..."}` in Go or throw `BridgeError` in TypeScript for a named failure. Other handler errors become `HANDLER_ERROR`. Register each request name once. There is no handler discovery, dependency injection container, or plugin loader.

## Layout

```text
addon/
  behavior_pack/manifest.json  MCP-generated pack identity
  src/main.ts                 initialization
  src/bridge/                 protocol, request routing, Minecraft adapter
  src/features.ts             example game handlers and triggers
server/
  cmd/bridge/                 application startup
  internal/bridge/            protocol and request routing
  internal/bedrock/           Minecraft socket adapter
  internal/features/          example app handlers
  internal/dashboard/         embedded browser interface
test/protocol.json            shared Go/TypeScript protocol fixtures
```

To copy the scaffold: change package/module names, pack name and description, generate fresh manifest UUIDs with the Script API MCP, and choose your namespace and prefix in both Minecraft adapters. Update Go imports if the module name changes. Replace the feature examples. The bridge requires no changes for new request names.

Build output is ignored. Builds do not modify manifests. No deployment path is stored in source; manually copy the built behavior pack, or use the Script API MCP's deploy operation with your installation path.

## Contract and limits

```json
{"v":1,"kind":"request","id":"unique-id","name":"game.time","data":null}
{"v":1,"kind":"response","id":"unique-id","data":{"ticks":1200}}
{"v":1,"kind":"response","id":"unique-id","error":{"code":"INVALID_DATA","message":"Expected text"}}
{"v":1,"kind":"event","name":"game.player_joined","data":{"name":"Steve"}}
```

Data can be any JSON value, including arrays or `null`. Requests have IDs; events do not. Responses contain either data or an error. IDs must be nonempty and at most 128 UTF-8 bytes. Unsupported versions and malformed envelopes are rejected; ordinary game messages are ignored.

Each encoded envelope is limited to 2,048 UTF-8 bytes, conservatively within the `/scriptevent` payload limit. Oversized requests fail before transmission; oversized results produce a compact error. There is no chunking, streaming, persistence, replay, or automatic request retry.

Each endpoint allows 16 pending outbound requests and 16 active request handlers. Requests time out after 10 seconds; add-on timers use 200 game ticks, so paused or lagging worlds delay the timeout. Shorter Go context deadlines are respected. A timeout does not prove an action was cancelled or never executed. Late replies are ignored. Disconnect fails the app's pending requests; the add-on learns failure through its timeout.

The app listens only on `127.0.0.1:8765`, accepts one Minecraft connection, and checks browser hosts/origins. Remote/LAN deployment needs a different security design. Browser traffic history is limited to 100 entries per tab and resets on reload; slow browser clients are disconnected instead of accumulating an unlimited queue.

## Dashboard API

- `POST /api/request`, JSON `{target: "app" | "game", name, data}` → `{data}` or `{error: {code, message}}`.
- `GET /api/live`, browser WebSocket → `{type: "state" | "frame" | "error", data}`. Frame data includes `{direction: "in" | "out", frame}`; directions are relative to the Go app.
- `GET /`, dashboard and local static assets.

## Verify

```powershell
npm run typecheck
npm test
npm run build
cd server
go vet ./...
```

The tests use a simulated Minecraft socket and mocked Script API. Real-game acceptance: activate the pack, connect, request `game.time`, run the demo command, observe a player event, restart the app, and reconnect. API compilation and simulations do not establish compatibility with an actual Minecraft installation; record the game version when performing that smoke test.
