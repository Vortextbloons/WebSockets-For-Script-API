import { system, world } from "@minecraft/server";
import { withContract } from "script-api-bridge";
import type { MinecraftConnection } from "script-api-bridge/minecraft";
import { contract } from "./contract";

export function registerFeatures(connection: MinecraftConnection, namespace: string): () => void {
  const api = withContract(connection.bridge, contract);
  let closed = false;

  const stopTime = api.handle("game.time", () => ({ ticks: world.getAbsoluteTime() }));

  const stopSpawn = connection.subscribe(world.afterEvents.playerSpawn, ({ player, initialSpawn }) => {
    if (initialSpawn) api.emit("game.player_joined", { name: player.name });
  });

  const stopDemo = connection.subscribe(system.afterEvents.scriptEventReceive, (event) => {
    if (event.id !== `${namespace}:demo`) return;
    api.request("app.echo", { text: event.message }).then(
      (result) => { if (!closed) world.sendMessage(`Echo: ${JSON.stringify(result)}`); },
      (error) => { if (!closed) world.sendMessage(`Echo failed: ${error.message}`); },
    );
  }, { namespaces: [namespace] });
  return connection.onClose(() => {
    closed = true;
    stopDemo();
    stopSpawn();
    stopTime();
  });
}
