import assert from "node:assert/strict";
import { test } from "node:test";
import vm from "node:vm";
import { buildSync } from "esbuild";

test("Minecraft adapter uses say, filters script event sources, and handles examples", async () => {
  const output = buildSync({ entryPoints: ["src/main.ts"], bundle: true, platform: "node", format: "cjs", write: false, external: ["@minecraft/server"] });
  const listeners = [];
  const startup = [];
  const commands = [];
  const messages = [];
  let spawn;
  const context = {
    console, module: { exports: {} }, exports: {},
    require: () => ({
      ScriptEventSource: { Server: "Server" },
      system: {
        run: (callback) => startup.push(callback), runTimeout: () => 1, clearRun() {},
        afterEvents: { scriptEventReceive: { subscribe: (callback) => listeners.push(callback) } },
      },
      world: {
        getAbsoluteTime: () => 1234,
        getDimension: () => ({ runCommand: (command) => commands.push(command) }),
        sendMessage: (message) => messages.push(message),
        afterEvents: { playerSpawn: { subscribe: (callback) => { spawn = callback; } } },
      },
    }),
  };
  vm.runInNewContext(output.outputFiles[0].text, context);
  const frames = () => commands.map((command) => {
    assert.match(command, /^say §8\[SC\] /);
    assert.equal(command.includes("\n"), false);
    return JSON.parse(command.slice("say §8[SC] ".length));
  });
  const receive = async (event) => {
    listeners.forEach((listener) => listener(event));
    await new Promise(setImmediate);
  };
  startup.forEach((callback) => callback());
  assert.equal(frames()[0].name, "bridge.started");
  const hello = JSON.stringify({ v: 1, kind: "request", id: "hello", name: "bridge.hello", data: null });
  await receive({ id: "scaffold:message", sourceType: "Entity", message: hello });
  assert.equal(commands.length, 1);
  await receive({ id: "scaffold:message", sourceType: "Server", message: hello });
  assert.deepEqual(frames()[1].data, { protocol: 1 });
  await receive({ id: "scaffold:message", sourceType: "Server", message: JSON.stringify({ v: 1, kind: "request", id: "time", name: "game.time", data: null }) });
  assert.deepEqual(frames()[2].data, { ticks: 1234 });
  await receive({ id: "scaffold:demo", message: 'Quotes " and \\ and\nnewlines 😀' });
  const request = frames()[3];
  assert.equal(request.name, "app.echo");
  await receive({ id: "scaffold:message", sourceType: "Server", message: JSON.stringify({ v: 1, kind: "response", id: request.id, data: request.data }) });
  assert.match(messages[0], /Echo:/);
  spawn({ initialSpawn: true, player: { name: "Steve" } });
  assert.equal(frames()[4].name, "game.player_joined");
});
