# script-api-bridge

Small JSON requests and events, with optional schemas. The core has no Minecraft imports; the optional Minecraft adapter uses stable `@minecraft/server` 2.9.0, supplied by your add-on.

```typescript
import { BridgeError, defineContract, schema, withContract } from "script-api-bridge";
import { createMinecraftBridge } from "script-api-bridge/minecraft";

const contract = defineContract({
  requests: {
    "game.double": {
      request: schema.number(),
      response: schema.number(),
    },
    "app.example": {
      request: schema.object({ value: schema.number() }),
      response: schema.object({ received: schema.boolean() }),
    },
  },
  events: {
    "game.changed": schema.object({ value: schema.number() }),
  },
});

const connection = createMinecraftBridge({ namespace: "myaddon", prefix: "[MY] " });
const api = withContract(connection.bridge, contract);

api.handle("game.double", (value) => value * 2);
const stop = api.on("game.changed", (data) => console.warn(JSON.stringify(data)));
api.request("app.example", { value: 2 }).then(
  (data) => console.warn(JSON.stringify(data)),
  (error) => console.warn(error.message),
);
api.emit("game.changed", { value: 3 });
stop();
connection.close();
```

## Typed contracts

`defineContract` lists every request and event with its schema. `withContract(bridge, contract)` returns a typed view:

- `handle` validates input before your handler runs and validates the result before it is sent. A bad result is reported as `HANDLER_ERROR`.
- `request` validates the data before sending it and the reply when it arrives.
- `on` and `emit` validate event payloads. A bad incoming payload is reported through `onError` and does not reach the listener.

Names outside the contract throw immediately, so typos fail during development.

## Schemas

`schema` builds validators: `string`, `number`, `boolean`, `null`, `literal`, `array`, `object`, and `optional` for object fields. Failures throw `INVALID_DATA` with the path to the bad field, such as `Expected string at tags[1]`. Object output keeps only declared fields, so newer peers can add fields without breaking you.

Without a contract, `bridge.handle(name, handler)` still accepts any JSON. Validate inside the handler and throw `BridgeError` for named failures.

## Connections and cleanup

`createMinecraftBridge` returns a connection:

- `connection.bridge` is the `Bridge` used for requests and events.
- `connection.subscribe(signal, callback, options?)` subscribes to a Script API signal. `close()` unsubscribes it. The returned function unsubscribes early.
- `connection.onClose(cleanup)` registers cleanup for `close()`. Cleanups run last-in, first-out, and run immediately if the connection is already closed. Errors in cleanups go to `onError`.
- `connection.close()` runs all cleanups, removes the Script API subscription and startup timer, cancels request timers, rejects pending requests, and discards late handler results. It is safe to call twice.

Use the same namespace and prefix in the Go adapter. `onError` optionally handles adapter errors.

## Errors and heartbeats

Standard codes are exported as `errorCodes` and typed as `ErrorCode`: `INVALID_FRAME`, `INVALID_DATA`, `PAYLOAD_TOO_LARGE`, `UNKNOWN_REQUEST`, `HANDLER_ERROR`, `LISTENER_ERROR`, `TIMEOUT`, `BUSY`, `DISCONNECTED`, `NOT_READY`, `COMMAND_REJECTED`, `INCOMPATIBLE`, and `INTERNAL_ERROR`. Applications can use their own codes.

Every bridge answers `bridge.ping` for heartbeats and `bridge.hello` for the handshake. Listener failures are isolated: one throwing listener does not stop the others or the connection. They are reported through `onError` as `LISTENER_ERROR`.

For other transports, import `Bridge`, pass a sender `(json: string) => void`, an options object with optional `onError`, and a `Clock` with `set(callback, milliseconds)` / `clear(id)`, then call `receive(json)` for incoming messages. Call `bridge.close()` when that connection ends.

Requests default to 10 seconds; `request(name, data, timeoutMs)` can override this. Envelopes are limited to 2,048 UTF-8 bytes, with 16 pending requests and 16 active handlers. Events have no response. Timeouts do not cancel remote work.

Bundle your add-on with `@minecraft/server` external. Import from the two public entry points above; generated JavaScript and declarations are included in the npm archive.
