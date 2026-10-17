import assert from "node:assert/strict";
import { resolve } from "node:path";
import { test } from "node:test";
import vm from "node:vm";
import { buildSync } from "esbuild";

// Builds the real connection and feature code against a fake Script API that tracks live subscriptions and timers.
function environment() {
  const signalSets = { scriptEventReceive: new Set(), playerSpawn: new Set() };
  const signal = (set) => ({
    subscribe(callback) { set.add(callback); return callback; },
    unsubscribe(callback) { if (!set.delete(callback)) throw new Error("callback was not subscribed"); },
  });
  const timers = new Map();
  const commands = [];
  let next = 0;
  const playerSpawn = signal(signalSets.playerSpawn);
  const scriptEventReceive = signal(signalSets.scriptEventReceive);
  const output = buildSync({
    stdin: {
      contents: 'export { createMinecraftBridge } from "script-api-bridge/minecraft";\nexport { registerFeatures } from "./src/features";',
      resolveDir: resolve(import.meta.dirname, ".."),
      loader: "ts",
      sourcefile: "entry.ts",
    },
    bundle: true, platform: "node", format: "cjs", write: false, external: ["@minecraft/server"],
  });
  const context = {
    console, module: { exports: {} }, exports: {},
    require: () => ({
      ScriptEventSource: { Server: "Server" },
      system: {
        run: (callback) => { timers.set(++next, callback); return next; },
        runTimeout: (callback) => { timers.set(++next, callback); return next; },
        clearRun: (id) => { timers.delete(id); },
        afterEvents: { scriptEventReceive },
      },
      world: {
        getAbsoluteTime: () => 1,
        getDimension: () => ({ runCommand: (command) => commands.push(command) }),
        sendMessage() {},
        afterEvents: { playerSpawn },
      },
    }),
  };
  vm.runInNewContext(output.outputFiles[0].text, context);
  return { ...context.module.exports, signalSets, timers, commands, playerSpawn };
}

test("close removes every Script API subscription and pending timer the add-on created", () => {
  const { createMinecraftBridge, registerFeatures, signalSets, timers } = environment();
  const connection = createMinecraftBridge({ namespace: "scaffold", prefix: "§8[SC] " });
  registerFeatures(connection, "scaffold");
  assert.equal(signalSets.scriptEventReceive.size, 2, "adapter and demo subscriptions");
  assert.equal(signalSets.playerSpawn.size, 1);
  assert.equal(timers.size, 1, "startup announcement is still pending");
  connection.close();
  connection.close();
  assert.equal(signalSets.scriptEventReceive.size, 0);
  assert.equal(signalSets.playerSpawn.size, 0);
  assert.equal(timers.size, 0);
});

test("after close, a spawn no longer reaches the bridge", () => {
  const { createMinecraftBridge, registerFeatures, commands, signalSets } = environment();
  const connection = createMinecraftBridge({ namespace: "scaffold", prefix: "§8[SC] " });
  registerFeatures(connection, "scaffold");
  connection.close();
  const before = commands.length;
  for (const callback of [...signalSets.playerSpawn]) callback({ initialSpawn: true, player: { name: "Steve" } });
  assert.equal(signalSets.playerSpawn.size, 0);
  assert.equal(commands.length, before);
});

test("onClose cleanups run last-in first-out once, and late registrations run immediately", () => {
  const { createMinecraftBridge } = environment();
  const connection = createMinecraftBridge({ namespace: "scaffold", prefix: "§8[SC] " });
  const order = [];
  connection.onClose(() => order.push("first"));
  connection.onClose(() => order.push("second"));
  const dispose = connection.onClose(() => order.push("disposed early"));
  dispose();
  dispose();
  assert.deepEqual(order, ["disposed early"], "dispose runs the cleanup once, immediately");
  connection.close();
  assert.deepEqual(order, ["disposed early", "second", "first"]);
  connection.onClose(() => order.push("late"));
  assert.deepEqual(order, ["disposed early", "second", "first", "late"]);
});

test("subscribe returns an early unsubscribe and failing cleanups are reported without stopping others", () => {
  const reported = [];
  const { createMinecraftBridge, playerSpawn, signalSets } = environment();
  const connection = createMinecraftBridge({ namespace: "scaffold", prefix: "§8[SC] ", onError: (error) => reported.push(error) });
  let spawns = 0;
  const stop = connection.subscribe(playerSpawn, () => { spawns++; });
  assert.equal(signalSets.playerSpawn.size, 1);
  stop();
  assert.equal(signalSets.playerSpawn.size, 0);
  connection.onClose(() => { throw new Error("cleanup bug"); });
  let ran = false;
  connection.onClose(() => { ran = true; });
  connection.close();
  assert.equal(ran, true);
  assert.equal(reported.length, 1);
  assert.match(String(reported[0]), /cleanup bug/);
  assert.equal(spawns, 0);
});
