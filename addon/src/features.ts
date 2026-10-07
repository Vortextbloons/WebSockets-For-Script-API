import { system, world } from "@minecraft/server";
import { Bridge } from "./bridge/bridge";
import { NAMESPACE } from "./bridge/minecraft";

export function registerFeatures(bridge: Bridge): void {
  bridge.handle("game.time", () => ({ ticks: world.getAbsoluteTime() }));

  world.afterEvents.playerSpawn.subscribe(({ player, initialSpawn }) => {
    if (initialSpawn) bridge.emit("game.player_joined", { name: player.name });
  });

  system.afterEvents.scriptEventReceive.subscribe((event) => {
    if (event.id !== `${NAMESPACE}:demo`) return;
    bridge.request("app.echo", { text: event.message }).then(
      (result) => world.sendMessage(`Echo: ${JSON.stringify(result)}`),
      (error) => world.sendMessage(`Echo failed: ${error.message}`),
    );
  }, { namespaces: [NAMESPACE] });
}
