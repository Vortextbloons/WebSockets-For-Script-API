import { ScriptEventSource, system, world } from "@minecraft/server";
import { Bridge } from "./bridge.js";

export type MinecraftOptions = {
  namespace?: string;
  prefix?: string;
  onError?: (error: unknown) => void;
};

export function createMinecraftBridge(options: MinecraftOptions = {}): { bridge: Bridge; close(): void } {
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
  });
  const listener = system.afterEvents.scriptEventReceive.subscribe((event) => {
    if (event.id === `${namespace}:message` && event.sourceType === ScriptEventSource.Server) {
      bridge.receive(event.message).catch(onError);
    }
  }, { namespaces: [namespace] });
  const startup = system.run(() => {
    try { bridge.emit("bridge.started"); } catch (error) { onError(error); }
  });
  let closed = false;
  return {
    bridge,
    close() {
      if (closed) return;
      closed = true;
      system.clearRun(startup);
      system.afterEvents.scriptEventReceive.unsubscribe(listener);
      bridge.close();
    },
  };
}
