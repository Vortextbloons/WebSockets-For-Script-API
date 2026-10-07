import { ScriptEventSource, system, world } from "@minecraft/server";
import { Bridge } from "./bridge";

export const NAMESPACE = "scaffold";
export const PREFIX = "§8[SC] ";

export function createBridge(): Bridge {
  return new Bridge((raw) => {
    world.getDimension("overworld").runCommand(`say ${PREFIX}${raw}`);
  }, {
    set: (callback, milliseconds) => system.runTimeout(callback, Math.ceil(milliseconds / 50)),
    clear: (id) => system.clearRun(id),
  });
}

export function startBridge(bridge: Bridge): void {
  system.afterEvents.scriptEventReceive.subscribe((event) => {
    if (event.id === `${NAMESPACE}:message` && event.sourceType === ScriptEventSource.Server) {
      bridge.receive(event.message).catch((error) => console.warn(`Bridge: ${error}`));
    }
  }, { namespaces: [NAMESPACE] });
  system.run(() => {
    try { bridge.emit("bridge.started"); } catch (error) { console.warn(`Bridge: ${error}`); }
  });
}
