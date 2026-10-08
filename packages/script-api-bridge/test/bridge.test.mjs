import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { Bridge, decode, encode } from "script-api-bridge";

const fixtures = JSON.parse(readFileSync("../go/testdata/protocol.json", "utf8"));

function setup(sender) {
  let next = 0;
  const timers = new Map();
  const sent = [];
  const bridge = new Bridge(sender ?? ((raw) => sent.push(JSON.parse(raw))), {
    set(callback) { timers.set(++next, callback); return next; },
    clear(id) { timers.delete(id); },
  });
  return { bridge, sent, timers };
}

test("shared envelopes round-trip and invalid shapes are rejected", () => {
  for (const frame of fixtures.valid) assert.deepEqual(decode(encode(frame)), frame);
  for (const frame of fixtures.invalid) assert.throws(() => decode(JSON.stringify(frame)));
  assert.throws(() => decode("{"));
  assert.throws(() => encode({ v: 1, kind: "event", name: "large", data: "😀".repeat(600) }), { code: "PAYLOAD_TOO_LARGE" });
});

test("simultaneous requests correlate reverse-order replies and clear timers", async () => {
  const { bridge, sent, timers } = setup();
  const first = bridge.request("app.echo", { text: "first" });
  const second = bridge.request("app.echo", { text: "second" });
  assert.notEqual(sent[0].id, sent[1].id);
  await bridge.receive(JSON.stringify({ v: 1, kind: "response", id: sent[1].id, data: "second" }));
  await bridge.receive(JSON.stringify({ v: 1, kind: "response", id: sent[0].id, data: "first" }));
  assert.deepEqual(await Promise.all([first, second]), ["first", "second"]);
  assert.equal(timers.size, 0);
});

test("timeout and transport errors release pending requests; late replies are ignored", async () => {
  const { bridge, sent, timers } = setup();
  const pending = bridge.request("slow");
  const rejection = assert.rejects(pending, { code: "TIMEOUT" });
  const expire = [...timers.values()][0];
  timers.clear();
  expire();
  await rejection;
  await bridge.receive(JSON.stringify({ v: 1, kind: "response", id: sent[0].id, data: null }));
  const failing = setup(() => { throw new Error("Socket failed"); });
  await assert.rejects(failing.bridge.request("app.echo"), /Socket failed/);
  assert.equal(failing.timers.size, 0);
  await assert.rejects(bridge.request("large", "x".repeat(3000)), { code: "PAYLOAD_TOO_LARGE" });
  assert.equal(timers.size, 0);
});

test("handlers return named errors and replace oversized results with compact errors", async () => {
  const { bridge, sent } = setup();
  bridge.handle("large", () => "x".repeat(3000));
  bridge.handle("fail", () => { throw new Error("Feature failed"); });
  for (const name of ["missing", "large", "fail"]) {
    await bridge.receive(JSON.stringify({ v: 1, kind: "request", id: name, name, data: null }));
  }
  assert.deepEqual(sent.map((frame) => frame.error.code), ["UNKNOWN_REQUEST", "PAYLOAD_TOO_LARGE", "HANDLER_ERROR"]);
  assert.throws(() => bridge.handle("large", () => null), /duplicate/);
});

test("event unsubscribe stops delivery and events do not generate replies", async () => {
  const { bridge, sent } = setup();
  const received = [];
  const stop = bridge.on("changed", (data) => received.push(data));
  const frame = JSON.stringify({ v: 1, kind: "event", name: "changed", data: [1, true] });
  await bridge.receive(frame);
  stop();
  await bridge.receive(frame);
  assert.deepEqual(received, [[1, true]]);
  assert.equal(sent.length, 0);
  bridge.emit("changed", null);
  assert.equal(sent[0].kind, "event");
});

test("pending requests and active handlers are bounded", async () => {
  const { bridge, sent } = setup();
  const requests = Array.from({ length: 16 }, () => bridge.request("app.echo"));
  await assert.rejects(bridge.request("app.echo"), { code: "BUSY" });
  for (const frame of sent.slice()) {
    await bridge.receive(JSON.stringify({ v: 1, kind: "response", id: frame.id, data: null }));
  }
  await Promise.all(requests);
  sent.length = 0;
  const release = [];
  bridge.handle("wait", () => new Promise((resolve) => release.push(resolve)));
  const handlers = Array.from({ length: 16 }, (_, i) => bridge.receive(JSON.stringify({ v: 1, kind: "request", id: String(i), name: "wait", data: null })));
  await bridge.receive(JSON.stringify({ v: 1, kind: "request", id: "overflow", name: "wait", data: null }));
  assert.equal(sent[0].error.code, "BUSY");
  release.forEach((resolve) => resolve(null));
  await Promise.all(handlers);
});

test("close releases timers and listeners and suppresses late handler replies", async () => {
  const { bridge, sent, timers } = setup();
  let calls = 0;
  const stop = bridge.on("changed", () => { calls++; });
  const pending = bridge.request("never");
  const rejected = assert.rejects(pending, { code: "DISCONNECTED" });
  let finish;
  bridge.handle("wait", () => new Promise((resolve) => { finish = resolve; }));
  const handler = bridge.receive(JSON.stringify({ v: 1, kind: "request", id: "wait", name: "wait", data: null }));
  bridge.close();
  bridge.close();
  await rejected;
  assert.equal(timers.size, 0);
  stop();
  await bridge.receive(JSON.stringify({ v: 1, kind: "event", name: "changed", data: null }));
  assert.equal(calls, 0);
  finish(null);
  await handler;
  assert.equal(sent.length, 1);
  await assert.rejects(bridge.request("another"), { code: "DISCONNECTED" });
  assert.throws(() => bridge.emit("changed"), { code: "DISCONNECTED" });
});
