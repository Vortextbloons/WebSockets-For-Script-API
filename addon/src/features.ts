import { system, world } from "@minecraft/server";
import type { Bridge } from "script-api-bridge";

export function registerFeatures(bridge: Bridge, namespace: string): void {
  bridge.handle("game.time", () => ({ ticks: world.getAbsoluteTime() }));

  world.afterEvents.playerSpawn.subscribe(({ player, initialSpawn }) => {
    if (initialSpawn) bridge.emit("game.player_joined", { name: player.name });
  });

  system.afterEvents.scriptEventReceive.subscribe((event) => {
    if (event.id !== `${namespace}:demo`) return;
    bridge.request("app.echo", { text: event.message }).then(
      (result) => world.sendMessage(`Echo: ${JSON.stringify(result)}`),
      (error) => world.sendMessage(`Echo failed: ${error.message}`),
    );
  }, { namespaces: [namespace] });
}
