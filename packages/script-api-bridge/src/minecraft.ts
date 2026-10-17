import { ScriptEventSource, system, world } from "@minecraft/server";
import { Bridge } from "./bridge.js";

export type MinecraftOptions = {
  namespace?: string;
  prefix?: string;
  onError?: (error: unknown) => void;
};

// Any Script API signal with subscribe/unsubscribe, such as world.afterEvents.playerSpawn.
export type Signal<T> = {
  subscribe(callback: (event: T) => void, options?: any): unknown;
  unsubscribe(callback: (event: T) => void): void;
};

export type MinecraftConnection = {
  bridge: Bridge;
  // Subscribes to a Script API signal. close() unsubscribes it. The returned function unsubscribes now.
  subscribe<T>(signal: Signal<T>, callback: (event: T) => void, options?: unknown): () => void;
  // Registers cleanup for close(), which runs cleanups last-in first-out. Runs now if already closed.
  // The returned function runs the cleanup now, once, and removes it from the close list.
  onClose(cleanup: () => void): () => void;
  close(): void;
};

export function createMinecraftBridge(options: MinecraftOptions = {}): MinecraftConnection {
  const namespace = options.namespace ?? "scaffold";
  const prefix = options.prefix ?? "§8[SC] ";
  if (!/^[a-z0-9_.-]+$/.test(namespace) || !prefix || /[\r\n]/.test(prefix)) {
    throw new Error("Expected a Minecraft namespace and a non-empty single-line prefix");
  }
  const onError = options.onError ?? ((error: unknown) => console.warn(`Bridge: ${error}`));
  const bridge = new Bridge((raw) => {
    world.getDimension("overworld").runCommand(`say ${prefix}${raw}`);
  }, {
    set: (callback, milliseconds) => system.runTimeout(callback, Math.ceil(milliseconds / 50)),
    clear: (id) => system.clearRun(id),
  }, { onError });

  // Every cleanup the connection owns. close() runs them last-in, first-out, then closes the bridge.
  const disposers = new Set<() => void>();
  let closed = false;
  const track = (cleanup: () => void): (() => void) => {
    let done = false;
    const dispose = () => {
      if (done) return;
      done = true;
      disposers.delete(dispose);
      try {
        cleanup();
      } catch (error) {
        onError(error);
      }
    };
    if (closed) dispose();
    else disposers.add(dispose);
    return dispose;
  };
  const subscribe = <T>(signal: Signal<T>, callback: (event: T) => void, subscriptionOptions?: unknown): (() => void) => {
    // Some signals take no options argument, so only pass one when it is given.
    if (subscriptionOptions === undefined) signal.subscribe(callback);
    else signal.subscribe(callback, subscriptionOptions);
    return track(() => signal.unsubscribe(callback));
  };

  const startup = system.run(() => {
    try { bridge.emit("bridge.started"); } catch (error) { onError(error); }
  });
  track(() => system.clearRun(startup));
  subscribe(system.afterEvents.scriptEventReceive, (event) => {
    if (event.id === `${namespace}:message` && event.sourceType === ScriptEventSource.Server) {
      bridge.receive(event.message).catch(onError);
    }
  }, { namespaces: [namespace] });

  return {
    bridge,
    subscribe,
    onClose: track,
    close() {
      if (closed) return;
      closed = true;
      for (const dispose of [...disposers].reverse()) dispose();
      bridge.close();
    },
  };
}
