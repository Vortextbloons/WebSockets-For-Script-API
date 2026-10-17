# Script API Bridge

Small reusable libraries for JSON requests and events, plus a Minecraft Bedrock + Go starter with a local dashboard. Feature names and JSON data belong to your application; the bridge handles routing, replies, timeouts, and connection health. [MIT licensed](LICENSE).

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

`npm run dev` builds the dashboard and starts the Go app. Copy the rebuilt pack and reload it after changes.

## Dashboard

The dashboard at `127.0.0.1:8765` shows connection state and health, and lets you send requests:

- **Status**: Minecraft state, heartbeat latency, time since the last packet, and missed heartbeats. When the connection ends, a notice gives the reason and what to do next.
- **Requests**: JSON formatting with syntax highlighting, validation messages, a copy button, and per-request timing from both the browser and the server.
- **History**: recent requests with results, stored in the browser. Re-run any of them.
- **Traffic**: live frames with latency for each round trip. Search names, IDs, and payloads. Filter by direction, kind, or errors, and hide heartbeats.
- **Export**: download the filtered traffic and request history as JSON or NDJSON.

Dashboard API: `POST /api/request` accepts `{target: "app" | "game", name, data}` and returns `{data, durationMs}` or `{error, durationMs}`. `GET /api/live` streams `{type: "state" | "frame" | "error", data, at}` over a separate browser WebSocket; `at` is the server's Unix time in milliseconds.

### Dashboard development

Install dependencies with `npm install`. For hot reload, run `npm run server` in one terminal and `npm run dev:dashboard` in another, then open <http://127.0.0.1:5173>. Vite proxies dashboard API and live WebSocket traffic to Go on port `8765`; Minecraft should still connect to `localhost:8765/ws`. For production, `npm run build:dashboard` writes static assets into the Go embedded directory; `npm run server` builds them before starting Go. Run `npm run check:dashboard` for Svelte/TypeScript checking and `npm run fix:dashboard` to format the Svelte sources.

## Reuse the libraries

| Location | Purpose |
| --- | --- |
| [packages/script-api-bridge](packages/script-api-bridge/README.md) | npm library: transport-independent TypeScript core, schemas, typed contracts, and Minecraft adapter |
| [packages/go](packages/go/README.md) | Go module: public `bridge` and `bedrock` packages |
| `addon/` and `server/` | Starter: manifest, bundling, example features, dashboard, startup |

The starter imports both libraries. Its local dependency paths can be replaced with published versions later.

For your own add-on, build and pack the npm library:

```powershell
npm pack ./packages/script-api-bridge
```

Install the resulting archive in your add-on, alongside `@minecraft/server`, and bundle with that Minecraft module external:

```typescript
import { defineContract, schema, withContract } from "script-api-bridge";
import { createMinecraftBridge } from "script-api-bridge/minecraft";

const contract = defineContract({
  requests: { "game.example": { request: schema.number(), response: schema.number() } },
});
const connection = createMinecraftBridge({ namespace: "myaddon", prefix: "[MY] " });
withContract(connection.bridge, contract).handle("game.example", (data) => data * 2);
```

Use matching values in Go:

```go
game, err := bedrock.New(bedrock.Config{Namespace: "myaddon", Prefix: "[MY] "})
```

Handle the error, register application handlers through `game.Register`, and mount `game.Handler` on your HTTP mux. Bind to a loopback address. Library setup examples are in the package READMEs above.

For other transports, instantiate `Bridge` with a sender and clock in TypeScript, or `bridge.New` with a sender in Go. Feed incoming JSON to `receive` / `Receive`. The core imports no Minecraft APIs and knows no application features.

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

Standard error codes: `INVALID_FRAME`, `INVALID_DATA`, `PAYLOAD_TOO_LARGE`, `UNKNOWN_REQUEST`, `HANDLER_ERROR`, `LISTENER_ERROR`, `TIMEOUT`, `BUSY`, `DISCONNECTED`, `NOT_READY`, `COMMAND_REJECTED`, `INCOMPATIBLE`, and `INTERNAL_ERROR`. Both libraries use the same names and check them against a shared fixture.

The starter listens on `127.0.0.1:8765`. Add-on frames appear in chat; keep data non-secret. Local operators and packs must be trusted; prefixes and source filters are not authentication.

## Connection health

The Go server pings the add-on with `bridge.ping` every 30 seconds (`bedrock.Config.Heartbeat`; negative disables pings). Each ping prints one reply line in Minecraft chat, like other add-on frames. Any answer counts as alive, including the `UNKNOWN_REQUEST` an older add-on sends. Three missed heartbeats in a row end the session.

When a connection ends, `GET /api/live` reports the reason, and the dashboard shows it:

| Reason | Meaning | What to do |
| --- | --- | --- |
| `MINECRAFT_CLOSED` | Minecraft closed the WebSocket, often because the world closed | Rejoin the world and run `/wsserver` again |
| `CONNECTION_LOST` | The socket dropped without a close frame | Check that Minecraft is running on this machine, then reconnect |
| `MESSAGE_TOO_LARGE` | Minecraft sent a message over 16 KB | Keep add-on messages small and reconnect |
| `WRITE_FAILED` | Commands could not be written to Minecraft | Check Minecraft, then reconnect |
| `HEARTBEAT_TIMEOUT` | Minecraft is connected, but the add-on stopped answering | Enable or reload the behavior pack; check Minecraft's content log for script errors |
| `SERVER_STOPPED` | The app stopped the session | Restart the app, then reconnect |

For `NOT_READY`, check pack activation and cheats, reload, and reconnect. The adapter requires Encrypted Websockets off. For `UNKNOWN_REQUEST`, check the destination and registered name. App restarts require manual reconnection.

## Extend and clean up

Add starter features in `addon/src/features.ts` and `server/internal/features/features.go`. Declare request and event shapes once in `addon/src/contract.ts`.

| Operation | TypeScript | Go |
| --- | --- | --- |
| Request a result | `request(name, data)` → promise | `Request(ctx, name, data)` → JSON bytes, error |
| Register a request | `handle(name, handler)` → unregister | `Handle(name, handler)` |
| Typed request and reply | `withContract(bridge, contract)` | `HandleTyped`, `RequestTyped`, `Parse` |
| Publish an event | `emit(name, data)` | `Emit(ctx, name, data)` |
| Listen for an event | `on(name, listener)` → unsubscribe | `On(name, listener)` → unsubscribe; `OnTyped` |

Request data and event payloads can be checked against schemas: `schema` builders in TypeScript and `Validate() error` methods in Go. Failures return `INVALID_DATA`. Register request names once. Handlers should honor their context and listeners should finish quickly.

Call `connection.close()` for the Minecraft adapter, `bridge.close()` for a standalone TypeScript bridge, or `Close()` in Go when that connection ends. In the Minecraft adapter, subscribe to Script API signals with `connection.subscribe` and register other teardown with `connection.onClose`; `close()` removes them all, along with the startup timer and pending requests. Go's `Close` rejects pending requests and releases listeners.

To copy the starter, rename the pack, generate fresh manifest UUIDs with the Script API MCP, change the matching namespace/prefix in both entry points, and replace the example features. Keep library code shared.

## Verify and release

```powershell
npm run typecheck
npm test
npm run build
```

Tests cover shared protocol fixtures, shared error codes, simulated Minecraft sockets, heartbeats and disconnect reasons, listener isolation, Script API cleanup, dashboard endpoints, and separate consumers of the packed npm library and public Go module. Before release, test both request directions and events in Minecraft, then restart and reconnect.

For a public release: choose the npm name/scope, give the Go module its repository import path, and replace the starter's local dependency paths with release versions. Use tags such as `packages/go/v0.1.0` for this nested Go module ([Go release guidance](https://go.dev/doc/modules/managing-source)). MIT license files, package exports, and declarations are included; no public release has been published.
