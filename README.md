# Script API Bridge

Small reusable libraries for JSON requests and events, plus a Minecraft Bedrock + Go starter with a local dashboard. Feature names and JSON data belong to your application; the bridge handles routing, replies, and timeouts. [MIT licensed](LICENSE).

## Start the example

Requires Node.js 22+, Go 1.22+, and Minecraft for Windows with `/wsserver`. The pack targets Minecraft 1.26.50+ and stable `@minecraft/server` 2.9.0. Real-game compatibility still needs a smoke test.

```powershell
npm install
npm run build
npm run server
```

Copy `addon/behavior_pack` into Minecraft's `development_behavior_packs`. Enable the pack and cheats, open [the dashboard](http://127.0.0.1:8765), and run:

```text
/wsserver localhost:8765/ws
```

Wait for **Minecraft ready**. Request `game.time` with `null` data in the dashboard. Run `/scriptevent scaffold:demo Hello` to test a request from Minecraft to the app. Initial player spawns publish `game.player_joined`.

`npm run dev` rebuilds and starts the app. Copy the rebuilt pack and reload it after changes.

## Reuse the libraries

| Location | Purpose |
| --- | --- |
| [packages/script-api-bridge](packages/script-api-bridge/README.md) | npm library: transport-independent TypeScript core and Minecraft adapter |
| [packages/go](packages/go/README.md) | Go module: public `bridge` and `bedrock` packages |
| `addon/` and `server/` | Starter: manifest, bundling, example features, dashboard, startup |

The starter imports both libraries. Its local dependency paths can be replaced with published versions later.

For your own add-on, build and pack the npm library:

```powershell
npm pack ./packages/script-api-bridge
```

Install the resulting archive in your add-on, alongside `@minecraft/server`, and bundle with that Minecraft module external:

```typescript
import { createMinecraftBridge } from "script-api-bridge/minecraft";

const connection = createMinecraftBridge({ namespace: "myaddon", prefix: "[MY] " });
connection.bridge.handle("game.example", (data) => ({ received: data }));
```

Use matching values in Go:

```go
game, err := bedrock.New(bedrock.Config{Namespace: "myaddon", Prefix: "[MY] "})
```

Handle the error, register application handlers through `game.Register`, and mount `game.Handler` on your HTTP mux. Bind to a loopback address. Library setup examples are in the package READMEs above.

For other transports, instantiate `Bridge` with a sender and scheduler in TypeScript, or `bridge.New` with a sender in Go. Feed incoming JSON to `receive` / `Receive`. The core imports no Minecraft APIs and knows no application features.

## Extend and clean up

Add starter features in `addon/src/features.ts` and `server/internal/features/features.go`.

| Operation | TypeScript | Go |
| --- | --- | --- |
| Request a result | `request(name, data)` → promise | `Request(ctx, name, data)` → JSON bytes, error |
| Register a request | `handle(name, handler)` | `Handle(name, handler)` |
| Publish an event | `emit(name, data)` | `Emit(ctx, name, data)` |
| Listen for an event | `on(name, listener)` → unsubscribe | `On(name, listener)` → unsubscribe |

Register request names once. Validate data inside handlers; throw `BridgeError` from `script-api-bridge`, or return `*bridge.Error`, for named failures. Go handlers should honor their context; listeners should finish quickly.

Call the returned unsubscribe function to stop a listener. Call `connection.close()` for the Minecraft adapter, `bridge.close()` for a standalone TypeScript bridge, or `Close()` in Go when that connection ends. Cleanup rejects pending requests and releases listeners; the Minecraft adapter also removes its Script API subscription and timers. Manage your own feature subscriptions separately.

To copy the starter, rename the pack, generate fresh manifest UUIDs with the Script API MCP, change the matching namespace/prefix in both entry points, and replace the example features. Keep library code shared.

## Transport and contract

Minecraft opens the WebSocket. Go sends a command packet containing `scriptevent <namespace>:message <json>`. The add-on responds through `say <prefix><json>`, received as a subscribed `PlayerMessage` event. Command acceptance does not complete a bridge request; its matching response does.

```json
{"v":1,"kind":"request","id":"unique","name":"game.time","data":null}
{"v":1,"kind":"response","id":"unique","data":{"ticks":1200}}
{"v":1,"kind":"response","id":"unique","error":{"code":"INVALID_DATA","message":"Expected text"}}
{"v":1,"kind":"event","name":"game.changed","data":{"value":3}}
```

- Any JSON data, including arrays and `null`; 2,048 UTF-8 bytes per complete envelope.
- 10-second requests by default. TypeScript accepts `request(name, data, timeoutMs)`; Go has `Options.Timeout` / `Config.Timeout` and context deadlines. Add-on timers follow game ticks.
- 16 pending requests and 16 active handlers per endpoint. Events have no reply or delivery guarantee.
- One world per Bedrock server instance. No retries, offline queue, replay, or persistence. Timeouts do not cancel remote work.

The starter listens on `127.0.0.1:8765`. Add-on frames appear in chat; keep data non-secret. Local operators and packs must be trusted; prefixes and source filters are not authentication.

Dashboard API: `POST /api/request` accepts `{target: "app" | "game", name, data}` and returns `{data}` or `{error}`. `GET /api/live` streams `{type: "state" | "frame" | "error", data}` over a separate browser WebSocket.

For `NOT_READY`, check pack activation and cheats, reload, and reconnect. The adapter requires Encrypted Websockets off. For `UNKNOWN_REQUEST`, check the destination and registered name. App restarts require manual reconnection.

## Verify and release

```powershell
npm run typecheck
npm test
npm run build
```

Tests cover shared protocol fixtures, simulated Minecraft sockets, cleanup, and separate consumers of the packed npm library and public Go module. Before release, test both request directions and events in Minecraft, then restart and reconnect.

For a public release: choose the npm name/scope, give the Go module its repository import path, and replace the starter's local dependency paths with release versions. Use tags such as `packages/go/v0.1.0` for this nested Go module ([Go release guidance](https://go.dev/doc/modules/managing-source)). MIT license files, package exports, and declarations are included; no public release has been published.
