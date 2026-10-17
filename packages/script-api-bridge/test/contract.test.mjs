import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { Bridge, defineContract, errorCodes, schema, withContract } from "script-api-bridge";

const fixtures = JSON.parse(readFileSync("../go/testdata/protocol.json", "utf8"));

function setup(options) {
  let next = 0;
  const timers = new Map();
  const sent = [];
  const bridge = new Bridge((raw) => sent.push(JSON.parse(raw)), {
    set(callback) { timers.set(++next, callback); return next; },
    clear(id) { timers.delete(id); },
  }, options);
  return { bridge, sent, timers };
}

const frame = (value) => JSON.stringify({ v: 1, ...value });

test("error codes match the shared fixture used by the Go package", () => {
  assert.deepEqual([...errorCodes].sort(), [...fixtures.errorCodes].sort());
});

test("bridge.ping answers heartbeats without application handlers", async () => {
  const { bridge, sent } = setup();
  await bridge.receive(frame({ kind: "request", id: "p1", name: "bridge.ping", data: null }));
  assert.deepEqual(sent, [{ v: 1, kind: "response", id: "p1", data: null }]);
});

test("handle returns an unregister function", async () => {
  const { bridge, sent } = setup();
  const remove = bridge.handle("temporary", () => 1);
  remove();
  await bridge.receive(frame({ kind: "request", id: "t", name: "temporary", data: null }));
  assert.equal(sent[0].error.code, "UNKNOWN_REQUEST");
  bridge.handle("temporary", () => 2);
});

test("a throwing listener is reported and does not stop other listeners", async () => {
  const reported = [];
  const { bridge } = setup({ onError: (error) => reported.push(error) });
  const calls = [];
  bridge.on("changed", () => { throw new Error("listener bug"); });
  bridge.on("changed", (data) => calls.push(data));
  await bridge.receive(frame({ kind: "event", name: "changed", data: 7 }));
  assert.deepEqual(calls, [7]);
  assert.equal(reported.length, 1);
  assert.equal(reported[0].code, "LISTENER_ERROR");
  assert.match(reported[0].message, /listener bug/);
});

test("a throwing error reporter does not break event delivery", async () => {
  const { bridge } = setup({ onError() { throw new Error("reporter bug"); } });
  bridge.on("changed", () => { throw new Error("listener bug"); });
  await bridge.receive(frame({ kind: "event", name: "changed", data: null }));
});

test("schema objects keep declared fields, drop unknown ones, and name the bad field", () => {
  const profile = schema.object({
    name: schema.string(),
    tags: schema.array(schema.string()),
    nickname: schema.optional(schema.string()),
  });
  assert.deepEqual(profile.parse({ name: "Steve", tags: ["a"], extra: true }), { name: "Steve", tags: ["a"] });
  assert.deepEqual(profile.parse({ name: "Alex", tags: [], nickname: "A" }), { name: "Alex", tags: [], nickname: "A" });
  assert.throws(() => profile.parse({ name: 5, tags: [] }), { code: "INVALID_DATA", message: "Expected string at name" });
  assert.throws(() => profile.parse({ name: "x", tags: ["a", 2] }), { code: "INVALID_DATA", message: "Expected string at tags[1]" });
  assert.throws(() => profile.parse({ tags: [] }), { code: "INVALID_DATA", message: "Missing name" });
  assert.throws(() => profile.parse(null), { code: "INVALID_DATA" });
  assert.equal(schema.literal("on").parse("on"), "on");
  assert.throws(() => schema.literal("on").parse("off"), { code: "INVALID_DATA" });
  assert.throws(() => schema.number().parse(Number.NaN), { code: "INVALID_DATA" });
});

const contract = defineContract({
  requests: {
    "app.echo": {
      request: schema.object({ text: schema.string() }),
      response: schema.object({ text: schema.string() }),
    },
    "game.time": { request: schema.null(), response: schema.object({ ticks: schema.number() }) },
  },
  events: {
    "game.player_joined": schema.object({ name: schema.string() }),
  },
});

test("typed handlers reject bad input before running and bad output before sending", async () => {
  const { bridge, sent } = setup();
  let ran = 0;
  const typed = withContract(bridge, contract);
  typed.handle("app.echo", (data) => { ran++; return { text: data.text.toUpperCase() }; });
  typed.handle("game.time", () => ({ ticks: "not a number" }));
  await bridge.receive(frame({ kind: "request", id: "ok", name: "app.echo", data: { text: "hi", extra: 1 } }));
  await bridge.receive(frame({ kind: "request", id: "bad", name: "app.echo", data: { text: 3 } }));
  await bridge.receive(frame({ kind: "request", id: "reply", name: "game.time", data: null }));
  assert.equal(ran, 1);
  assert.deepEqual(sent[0], { v: 1, kind: "response", id: "ok", data: { text: "HI" } });
  assert.equal(sent[1].error.code, "INVALID_DATA");
  assert.equal(sent[2].error.code, "HANDLER_ERROR");
  assert.match(sent[2].error.message, /game.time/);
});

test("typed requests validate outgoing input and incoming replies", async () => {
  const { bridge, sent } = setup();
  const typed = withContract(bridge, contract);
  await assert.rejects(typed.request("app.echo", { text: 1 }), { code: "INVALID_DATA" });
  assert.equal(sent.length, 0, "invalid input must not be sent");
  const reply = typed.request("game.time", null);
  await bridge.receive(frame({ kind: "response", id: sent[0].id, data: { ticks: 40 } }));
  assert.deepEqual(await reply, { ticks: 40 });
});

test("typed events validate payloads and send only declared data", async () => {
  const { bridge, sent } = setup({ onError: () => {} });
  const typed = withContract(bridge, contract);
  const names = [];
  typed.on("game.player_joined", (data) => names.push(data.name));
  await bridge.receive(frame({ kind: "event", name: "game.player_joined", data: { name: "Steve", id: 9 } }));
  await bridge.receive(frame({ kind: "event", name: "game.player_joined", data: { name: 4 } }));
  assert.deepEqual(names, ["Steve"]);
  typed.emit("game.player_joined", { name: "Alex", stray: true });
  assert.deepEqual(sent[0], { v: 1, kind: "event", name: "game.player_joined", data: { name: "Alex" } });
});

test("contract helpers reject names that are not declared", () => {
  const { bridge } = setup();
  const typed = withContract(bridge, contract);
  assert.throws(() => typed.handle("missing", () => null), /not in the contract: missing/);
  assert.throws(() => typed.emit("missing", {}), /not in the contract: missing/);
});

test("after close, typed requests reject as disconnected", async () => {
  const { bridge } = setup();
  const typed = withContract(bridge, contract);
  bridge.close();
  await assert.rejects(typed.request("game.time", null), { code: "DISCONNECTED" });
});
