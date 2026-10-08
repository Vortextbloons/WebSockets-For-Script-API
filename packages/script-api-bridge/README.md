# script-api-bridge

Small JSON requests and events. The core has no Minecraft imports; the optional Minecraft adapter uses stable `@minecraft/server` 2.9.0, supplied by your add-on.

```typescript
import { BridgeError } from "script-api-bridge";
import { createMinecraftBridge } from "script-api-bridge/minecraft";

const connection = createMinecraftBridge({ namespace: "myaddon", prefix: "[MY] " });
const { bridge } = connection;
bridge.handle("game.double", (data) => {
  if (typeof data !== "number") throw new BridgeError("INVALID_DATA", "Expected a number");
  return data * 2;
});
const stop = bridge.on("app.notice", (data) => console.warn(JSON.stringify(data)));
bridge.request("app.example", { value: 2 }).then(
  (data) => console.warn(JSON.stringify(data)),
  (error) => console.warn(error.message),
);
bridge.emit("game.changed", { value: 3 });
stop();
connection.close();
```

Use the same namespace and prefix in the Go adapter. `close()` removes the Script API subscription, cancels startup and request timers, rejects pending requests, and discards late handler results. Dispose your own feature subscriptions separately. `onError` optionally handles adapter errors.

For other transports, import `Bridge`, pass a sender `(json: string) => void` and a `Clock` with `set(callback, milliseconds)` / `clear(id)`, then call `receive(json)` for incoming messages. Call `bridge.close()` when that connection ends.

Requests default to 10 seconds; `request(name, data, timeoutMs)` can override this. Envelopes are limited to 2,048 UTF-8 bytes, with 16 pending requests and 16 active handlers. Events have no response. Timeouts do not cancel remote work.

Bundle your add-on with `@minecraft/server` external. Import from the two public entry points above; generated JavaScript and declarations are included in the npm archive.
