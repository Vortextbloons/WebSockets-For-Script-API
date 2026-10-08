import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { test } from "node:test";
import vm from "node:vm";

const root = resolve(import.meta.dirname, "..");
const temporaryRoot = realpathSync(tmpdir());

function workspace(t) {
  const path = mkdtempSync(join(temporaryRoot, "script-api-bridge-"));
  t.after(() => {
    assert.equal(dirname(realpathSync(path)), temporaryRoot);
    rmSync(path, { recursive: true, force: true });
  });
  return path;
}

function run(command, args, cwd) {
  const result = spawnSync(command, args, { cwd, encoding: "utf8", timeout: 120_000, windowsHide: true });
  assert.equal(result.status, 0, result.error?.message ?? result.stdout + result.stderr);
  return result.stdout;
}

test("packed npm library works in an independent core app and add-on", async (t) => {
  const path = workspace(t);
  const library = join(root, "packages/script-api-bridge");
  const npm = process.env.npm_execpath;
  assert.ok(npm, "Run this check with npm run test:consumer");
  const [pack] = JSON.parse(run(process.execPath, [npm, "pack", "--ignore-scripts", "--json", "--pack-destination", path], library));
  assert.ok(pack.files.some((file) => file.path === "LICENSE"));
  writeFileSync(join(path, "package.json"), JSON.stringify({
    private: true, type: "module", dependencies: { "script-api-bridge": `file:${join(path, pack.filename).replaceAll("\\", "/")}` },
  }));
  run(process.execPath, [npm, "install", "--ignore-scripts", "--no-audit", "--no-fund", "--offline"], path);
  writeFileSync(join(path, "core.mjs"), `
import assert from "node:assert/strict";
import { Bridge } from "script-api-bridge";
let next = 0;
const timers = new Map();
const clock = { set(callback, ms) { const id = ++next; timers.set(id, setTimeout(callback, ms)); return id; }, clear(id) { clearTimeout(timers.get(id)); timers.delete(id); } };
const a = new Bridge(raw => { void b.receive(raw); }, clock);
const b = new Bridge(raw => { void a.receive(raw); }, clock);
b.handle("example.echo", data => data);
assert.deepEqual(await a.request("example.echo", { values: [1, true, null] }), { values: [1, true, null] });
let received;
a.on("example.changed", data => { received = data; });
b.emit("example.changed", "hello");
assert.equal(received, "hello");
a.close(); b.close();
assert.equal(timers.size, 0);
`);
  run(process.execPath, [join(path, "core.mjs")], path);
  writeFileSync(join(path, "main.ts"), `
import { BridgeError, type Bridge } from "script-api-bridge";
import { createMinecraftBridge } from "script-api-bridge/minecraft";
export const connection = createMinecraftBridge({ namespace: "example", prefix: "[EX] " });
const bridge: Bridge = connection.bridge;
bridge.handle("example.double", data => {
  if (typeof data !== "number") throw new BridgeError("INVALID_DATA", "Expected a number");
  return data * 2;
});
`);
  writeFileSync(join(path, "tsconfig.json"), JSON.stringify({ compilerOptions: {
    target: "ES2020", module: "NodeNext", moduleResolution: "NodeNext", lib: ["ES2020"], strict: true, noEmit: true, types: [],
  }, include: ["main.ts"] }));
  run(process.execPath, [join(library, "node_modules/typescript/bin/tsc")], path);
  const { buildSync } = createRequire(join(root, "addon/package.json"))("esbuild");
  const output = buildSync({ absWorkingDir: path, entryPoints: ["main.ts"], bundle: true, format: "cjs", write: false, external: ["@minecraft/server"] });
  const listeners = new Set();
  const scheduled = new Map();
  const commands = [];
  let sequence = 0;
  const context = { console, module: { exports: {} }, exports: {}, require: () => ({
    ScriptEventSource: { Server: "Server" },
    system: {
      run(callback) { scheduled.set(++sequence, callback); return sequence; },
      runTimeout(callback) { scheduled.set(++sequence, callback); return sequence; },
      clearRun(id) { scheduled.delete(id); },
      afterEvents: { scriptEventReceive: {
        subscribe(callback, options) { assert.deepEqual(Array.from(options.namespaces), ["example"]); listeners.add(callback); return callback; },
        unsubscribe(callback) { assert.ok(listeners.delete(callback)); },
      } },
    },
    world: { getDimension: () => ({ runCommand(command) { commands.push(command); } }) },
  }) };
  vm.runInNewContext(output.outputFiles[0].text, context);
  const { connection } = context.module.exports;
  const message = JSON.stringify({ v: 1, kind: "request", id: "double", name: "example.double", data: 4 });
  for (const listener of listeners) {
    listener({ id: "other:message", sourceType: "Server", message });
    listener({ id: "example:message", sourceType: "Entity", message });
  }
  await new Promise(setImmediate);
  assert.equal(commands.length, 0);
  for (const listener of listeners) listener({ id: "example:message", sourceType: "Server", message });
  await new Promise(setImmediate);
  assert.equal(JSON.parse(commands[0].slice("say [EX] ".length)).data, 8);
  const pending = connection.bridge.request("example.never");
  const rejected = assert.rejects(pending, { code: "DISCONNECTED" });
  assert.match(commands[0], /^say \[EX\] /);
  connection.close();
  connection.close();
  assert.equal(listeners.size, 0);
  assert.equal(scheduled.size, 0);
  await rejected;
});

test("independent Go application imports the public core and Bedrock adapter", (t) => {
  const path = workspace(t);
  const library = join(root, "packages/go").replaceAll("\\", "/");
  writeFileSync(join(path, "go.mod"), `module example.com/consumer\n\ngo 1.22\n\nrequire scriptbridge v0.1.0\n\nreplace scriptbridge => "${library}"\n`);
  writeFileSync(join(path, "consumer_test.go"), `package consumer
import (
  "context"
  "encoding/json"
  "net/http"
  "testing"
  "scriptbridge/bridge"
  "scriptbridge/bedrock"
)
func TestPublicPackages(t *testing.T) {
  var a, b *bridge.Bridge
  a = bridge.New(func(ctx context.Context, frame bridge.Frame) error { raw, err := bridge.Encode(frame); if err != nil { return err }; return b.Receive(ctx, raw) }, bridge.Options{})
  b = bridge.New(func(ctx context.Context, frame bridge.Frame) error { raw, err := bridge.Encode(frame); if err != nil { return err }; return a.Receive(ctx, raw) }, bridge.Options{})
  defer a.Close(); defer b.Close()
  b.Handle("example.echo", func(_ context.Context, raw json.RawMessage) (any, error) { return raw, nil })
  data, err := a.Request(context.Background(), "example.echo", []any{1, true, nil})
  if err != nil || string(data) != "[1,true,null]" { t.Fatalf("%s: %v", data, err) }
  game, err := bedrock.New(bedrock.Config{Namespace: "example", Prefix: "[EX] "})
  if err != nil { t.Fatal(err) }
  defer game.Close()
  mux := http.NewServeMux(); mux.HandleFunc("GET /ws", game.Handler)
}
`);
  run("go", ["test", "-mod=mod", "./..."], path);
});
