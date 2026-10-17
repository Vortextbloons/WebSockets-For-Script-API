const MAX_TRAFFIC = 500;
const MAX_VISIBLE = 300;
const MAX_HISTORY = 50;
const MAX_ROUND_TRIPS = 100;
const HEARTBEAT = "bridge.ping";
const HISTORY_KEY = "script-api-bridge.history";

const el = {
  request: document.querySelector("#request"),
  target: document.querySelector("#target"),
  name: document.querySelector("#name"),
  data: document.querySelector("#data"),
  format: document.querySelector("#format"),
  send: document.querySelector("#send"),
  jsonError: document.querySelector("#json-error"),
  result: document.querySelector("#result"),
  resultMeta: document.querySelector("#result-meta"),
  copyResult: document.querySelector("#copy-result"),
  history: document.querySelector("#history"),
  historyEmpty: document.querySelector("#history-empty"),
  clearHistory: document.querySelector("#clear-history"),
  appLink: document.querySelector("#app-link"),
  state: document.querySelector("#state"),
  health: document.querySelector("#health"),
  notice: document.querySelector("#notice"),
  noticeTitle: document.querySelector("#notice-title"),
  noticeBody: document.querySelector("#notice-body"),
  search: document.querySelector("#search"),
  direction: document.querySelector("#direction"),
  kind: document.querySelector("#kind"),
  hideHeartbeats: document.querySelector("#hide-heartbeats"),
  frames: document.querySelector("#frames"),
  empty: document.querySelector("#empty"),
  counts: document.querySelector("#counts"),
  clear: document.querySelector("#clear"),
  export: document.querySelector("#export"),
  exportFormat: document.querySelector("#export-format"),
  latLast: document.querySelector("#lat-last"),
  latAvg: document.querySelector("#lat-avg"),
  latMax: document.querySelector("#lat-max"),
  latCount: document.querySelector("#lat-count"),
};

let latestState = null;
let appOnline = false;
let traffic = [];
let sequence = 0;
let renderScheduled = false;
let lastResult = null;
let attempts = 0;
let connectedBefore = false;
const openRequests = new Map();
const roundTrips = [];
const expanded = new Set();
let history = loadHistory();

const REASON_TITLES = {
  MINECRAFT_CLOSED: "Minecraft closed the connection",
  CONNECTION_LOST: "Connection to Minecraft was lost",
  MESSAGE_TOO_LARGE: "Minecraft sent a message that was too large",
  WRITE_FAILED: "Commands could not be sent to Minecraft",
  HEARTBEAT_TIMEOUT: "Minecraft stopped answering the add-on",
  SERVER_STOPPED: "The server stopped the connection",
};

// ---- small helpers -------------------------------------------------------

function make(tag, className = "", text = "") {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text) node.textContent = text;
  return node;
}

const clock = (ms) => new Date(ms).toLocaleTimeString();
const fileStamp = () => new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
const kindMatches = (entry, kind) => kind === "all" || (kind === "error" ? entry.failed : entry.kind === kind);

function ago(ms) {
  const seconds = Math.max(0, Math.round((Date.now() - ms) / 1000));
  return seconds < 60 ? `${seconds}s ago` : `${Math.round(seconds / 60)}m ago`;
}

function setPill(node, text, tone) {
  node.textContent = text;
  node.className = `pill ${tone}`;
}

function download(filename, text, type) {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const link = make("a");
  link.href = url;
  link.download = filename;
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

// JSON formatter. Every token is built with textContent, so traffic cannot inject markup.
function renderJSON(value) {
  const fragment = document.createDocumentFragment();
  writeJSON(fragment, value, 0);
  return fragment;
}

function token(parent, className, text) {
  parent.append(make("span", className, text));
}

function writeJSON(parent, value, depth) {
  const indent = "  ".repeat(depth);
  const inner = "  ".repeat(depth + 1);
  if (value === null) return token(parent, "j-null", "null");
  if (typeof value === "string") return token(parent, "j-str", JSON.stringify(value));
  if (typeof value === "number") return token(parent, "j-num", String(value));
  if (typeof value === "boolean") return token(parent, "j-bool", String(value));
  if (Array.isArray(value)) {
    if (value.length === 0) return parent.append("[]");
    parent.append("[\n");
    value.forEach((item, index) => {
      parent.append(inner);
      writeJSON(parent, item, depth + 1);
      parent.append(index < value.length - 1 ? ",\n" : "\n");
    });
    return parent.append(`${indent}]`);
  }
  const keys = Object.keys(value);
  if (keys.length === 0) return parent.append("{}");
  parent.append("{\n");
  keys.forEach((key, index) => {
    parent.append(inner);
    token(parent, "j-key", JSON.stringify(key));
    parent.append(": ");
    writeJSON(parent, value[key], depth + 1);
    parent.append(index < keys.length - 1 ? ",\n" : "\n");
  });
  parent.append(`${indent}}`);
}

// ---- request composer ----------------------------------------------------

el.target.addEventListener("change", () => {
  el.name.value = el.target.value === "app" ? "app.echo" : "game.time";
  el.data.value = el.target.value === "app" ? '{"text":"Hello from the dashboard"}' : "null";
  hideJSONError();
});

function showJSONError(error) {
  el.jsonError.textContent = `Invalid JSON: ${error.message}`;
  el.jsonError.hidden = false;
}

function hideJSONError() {
  el.jsonError.hidden = true;
  el.jsonError.textContent = "";
}

el.format.addEventListener("click", () => {
  try {
    el.data.value = JSON.stringify(JSON.parse(el.data.value), null, 2);
    hideJSONError();
  } catch (error) {
    showJSONError(error);
  }
});

el.request.addEventListener("submit", (event) => {
  event.preventDefault();
  let data;
  try {
    data = JSON.parse(el.data.value);
  } catch (error) {
    showJSONError(error);
    return;
  }
  hideJSONError();
  sendRequest(el.target.value, el.name.value.trim(), data, el.data.value);
});

async function sendRequest(target, name, data, dataText) {
  el.send.disabled = true;
  el.result.textContent = "Waiting for a result…";
  el.resultMeta.textContent = "";
  const started = performance.now();
  const at = Date.now();
  let body;
  try {
    const response = await fetch("/api/request", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target, name, data }),
    });
    body = await response.json();
  } catch (error) {
    body = { error: { code: "NETWORK", message: `Dashboard server unreachable: ${error.message}` } };
  } finally {
    el.send.disabled = false;
  }
  const item = {
    at,
    target,
    name,
    data: dataText,
    ok: !body.error,
    code: body.error ? body.error.code : "",
    durationMs: Math.round(performance.now() - started),
    serverMs: typeof body.durationMs === "number" ? body.durationMs : null,
    body,
  };
  addHistory(item);
  showResult(item);
}

function resultMeta(item) {
  const status = item.ok ? "OK" : item.code;
  const server = item.serverMs === null ? "" : ` · ${item.serverMs} ms in server`;
  return `${status} · ${item.durationMs} ms round trip${server} · ${clock(item.at)}`;
}

function showResult(item) {
  lastResult = item.body;
  el.result.replaceChildren(renderJSON(item.body));
  el.resultMeta.textContent = resultMeta(item);
}

el.copyResult.addEventListener("click", async () => {
  if (!lastResult) return;
  try {
    await navigator.clipboard.writeText(JSON.stringify(lastResult, null, 2));
    el.copyResult.textContent = "Copied";
  } catch {
    el.copyResult.textContent = "Copy failed";
  }
  setTimeout(() => { el.copyResult.textContent = "Copy"; }, 1200);
});

// ---- request history -----------------------------------------------------

function loadHistory() {
  try {
    const saved = JSON.parse(localStorage.getItem(HISTORY_KEY) || "[]");
    return Array.isArray(saved) ? saved.slice(0, MAX_HISTORY) : [];
  } catch {
    return [];
  }
}

// Stored request text can be edited or corrupted; fall back to null instead of throwing.
function parseStored(text) {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

function saveHistory() {
  try {
    localStorage.setItem(HISTORY_KEY, JSON.stringify(history));
  } catch {
    // Storage can be full or disabled; history still works for this page.
  }
}

function addHistory(item) {
  history = [item, ...history].slice(0, MAX_HISTORY);
  saveHistory();
  renderHistory();
}

function renderHistory() {
  el.historyEmpty.hidden = history.length > 0;
  el.history.replaceChildren(...history.map((item) => {
    const row = make("li", `history-item ${item.ok ? "ok" : "fail"}`);
    const open = make("button", "history-open");
    open.type = "button";
    open.append(make("span", "history-name", `${item.target} · ${item.name}`));
    open.append(make("span", "muted", `${item.ok ? "OK" : item.code} · ${item.durationMs} ms · ${clock(item.at)}`));
    open.addEventListener("click", () => {
      el.target.value = item.target;
      el.name.value = item.name;
      el.data.value = item.data;
      hideJSONError();
      showResult(item);
    });
    const again = make("button", "ghost small", "Run again");
    again.type = "button";
    again.addEventListener("click", () => sendRequest(item.target, item.name, parseStored(item.data), item.data));
    row.append(open, again);
    return row;
  }));
}

el.clearHistory.addEventListener("click", () => {
  history = [];
  saveHistory();
  renderHistory();
});

// ---- live connection -----------------------------------------------------

function setAppOnline(online) {
  appOnline = online;
  if (online) {
    el.appLink.textContent = "App live";
    el.appLink.className = "pill ok";
  } else {
    el.appLink.className = "pill bad";
  }
  renderState();
}

function connect() {
  const socket = new WebSocket(`ws://${location.host}/api/live`);
  socket.addEventListener("open", () => {
    attempts = 0;
    setAppOnline(true);
    if (connectedBefore) {
      addEntry({ type: "notice", payload: "Reconnected to the app. Traffic while the dashboard was disconnected was not recorded." });
    }
    connectedBefore = true;
  });
  socket.addEventListener("message", (event) => handleMessage(JSON.parse(event.data)));
  socket.addEventListener("close", () => {
    setAppOnline(false);
    attempts += 1;
    const delay = Math.min(10_000, 1_000 * 2 ** Math.min(attempts - 1, 3));
    el.appLink.textContent = `App offline · retrying in ${delay / 1000}s`;
    setTimeout(connect, delay);
  });
}

function handleMessage(message) {
  if (message.type === "state") {
    latestState = message.data;
    renderState();
    return;
  }
  if (message.type === "frame") {
    addEntry({ type: "frame", at: message.at, direction: message.data.direction, frame: message.data.frame });
    return;
  }
  if (message.type === "error") {
    addEntry({ type: "error", at: message.at, payload: message.data });
  }
}

// ---- traffic log ---------------------------------------------------------

function addEntry(input) {
  const entry = {
    seq: ++sequence,
    at: input.at || Date.now(),
    type: input.type,
    direction: input.direction || "",
    kind: "notice",
    name: "",
    id: "",
    failed: false,
    latencyMs: null,
    payload: input.payload,
  };
  if (input.type === "frame") {
    const frame = input.frame;
    entry.kind = frame.kind;
    entry.name = frame.name || "";
    entry.id = frame.id || "";
    entry.failed = Boolean(frame.error);
    entry.payload = frame;
    pairFrame(entry);
  } else if (input.type === "error") {
    entry.kind = "error";
    entry.failed = true;
  }
  entry.searchText = `${entry.kind} ${entry.name} ${entry.id} ${entry.direction} ${JSON.stringify(entry.payload)}`.toLowerCase();
  traffic.push(entry);
  if (traffic.length > MAX_TRAFFIC) {
    for (const removed of traffic.splice(0, traffic.length - MAX_TRAFFIC)) expanded.delete(removed.seq);
  }
  scheduleRender();
}

// A response is matched to its request by ID; the gap between the two server timestamps is the latency.
function pairFrame(entry) {
  const frame = entry.payload;
  if (!frame.id) return;
  if (frame.kind === "request") {
    openRequests.set(frame.id, entry);
    if (openRequests.size > 200) openRequests.delete(openRequests.keys().next().value);
    return;
  }
  if (frame.kind !== "response") return;
  const request = openRequests.get(frame.id);
  if (!request) return;
  openRequests.delete(frame.id);
  entry.name = request.name;
  entry.latencyMs = Math.max(0, entry.at - request.at);
  request.latencyMs = entry.latencyMs;
  roundTrips.push(entry.latencyMs);
  if (roundTrips.length > MAX_ROUND_TRIPS) roundTrips.shift();
}

function visibleEntries() {
  const query = el.search.value.trim().toLowerCase();
  const direction = el.direction.value;
  const kind = el.kind.value;
  const hideHeartbeats = el.hideHeartbeats.checked;
  return traffic.filter((entry) => {
    if (direction !== "all" && entry.direction !== direction) return false;
    if (!kindMatches(entry, kind)) return false;
    if (hideHeartbeats && entry.name === HEARTBEAT) return false;
    return !query || entry.searchText.includes(query);
  });
}

function directionLabel(entry) {
  if (entry.direction === "in") return "Minecraft → App";
  if (entry.direction === "out") return "App → Minecraft";
  return entry.type === "notice" ? "Dashboard" : "Minecraft";
}

function renderEntry(entry) {
  const row = make("li", `entry ${entry.failed ? "failed" : ""} dir-${entry.direction || "none"}`);
  const head = make("div", "entry-head");
  head.append(make("span", "time", clock(entry.at)));
  head.append(make("span", `badge kind-${entry.kind}`, entry.kind));
  head.append(make("span", "direction", directionLabel(entry)));
  head.append(make("span", "entry-name", entry.name || entry.id));
  if (entry.latencyMs !== null) head.append(make("span", "latency", `${entry.latencyMs} ms`));
  const details = make("details");
  details.open = expanded.has(entry.seq);
  details.addEventListener("toggle", () => {
    if (details.open) expanded.add(entry.seq);
    else expanded.delete(entry.seq);
  });
  details.append(make("summary", "", "Payload"));
  const body = make("pre", "json");
  body.append(entry.type === "notice" ? entry.payload : renderJSON(entry.payload));
  details.append(body);
  row.append(head, details);
  return row;
}

function renderStats() {
  const count = roundTrips.length;
  const sum = roundTrips.reduce((total, value) => total + value, 0);
  const ms = (value) => (value === null ? "–" : `${value} ms`);
  el.latLast.textContent = ms(count ? roundTrips[count - 1] : null);
  el.latAvg.textContent = ms(count ? Math.round(sum / count) : null);
  el.latMax.textContent = ms(count ? Math.max(...roundTrips) : null);
  el.latCount.textContent = String(count);
}

function renderTraffic() {
  renderScheduled = false;
  const visible = visibleEntries();
  const shown = visible.slice(-MAX_VISIBLE).reverse();
  el.frames.replaceChildren(...shown.map(renderEntry));
  el.empty.hidden = traffic.length > 0;
  const capped = visible.length > MAX_VISIBLE ? ` · showing newest ${MAX_VISIBLE}` : "";
  el.counts.textContent = `${visible.length} of ${traffic.length} entries${capped}`;
  renderStats();
}

function scheduleRender() {
  if (!renderScheduled) {
    renderScheduled = true;
    requestAnimationFrame(renderTraffic);
  }
}

for (const control of [el.search, el.direction, el.kind, el.hideHeartbeats]) {
  control.addEventListener("input", renderTraffic);
  control.addEventListener("change", renderTraffic);
}

el.clear.addEventListener("click", () => {
  traffic = [];
  openRequests.clear();
  roundTrips.length = 0;
  expanded.clear();
  renderTraffic();
});

el.export.addEventListener("click", () => {
  const rows = visibleEntries().map((entry) => ({
    time: new Date(entry.at).toISOString(),
    direction: entry.direction || null,
    kind: entry.kind,
    name: entry.name || null,
    id: entry.id || null,
    failed: entry.failed,
    latencyMs: entry.latencyMs,
    payload: entry.payload,
  }));
  const stamp = fileStamp();
  if (el.exportFormat.value === "ndjson") {
    download(`script-api-traffic-${stamp}.ndjson`, rows.map((row) => JSON.stringify(row)).join("\n") + "\n", "application/x-ndjson");
    return;
  }
  const report = {
    exportedAt: new Date().toISOString(),
    filters: {
      search: el.search.value,
      direction: el.direction.value,
      kind: el.kind.value,
      hideHeartbeats: el.hideHeartbeats.checked,
    },
    minecraft: latestState,
    traffic: rows,
    requests: history.map((item) => ({
      time: new Date(item.at).toISOString(),
      target: item.target,
      name: item.name,
      ok: item.ok,
      durationMs: item.durationMs,
      serverMs: item.serverMs,
      data: parseStored(item.data),
      response: item.body,
    })),
  };
  download(`script-api-traffic-${stamp}.json`, JSON.stringify(report, null, 2), "application/json");
});

// ---- status --------------------------------------------------------------

function showNotice(tone, title, lines) {
  el.notice.className = `notice ${tone}`;
  el.notice.hidden = false;
  el.noticeTitle.textContent = title;
  el.noticeBody.replaceChildren(...lines.map((line) => make("p", "", line)));
}

function renderState() {
  if (!appOnline) {
    setPill(el.state, "Minecraft unknown", "idle");
    showNotice("warn", "The app is offline", ["Minecraft status is unknown until the dashboard reconnects. Start the app if it has stopped."]);
    el.health.textContent = "";
    return;
  }
  const s = latestState;
  if (!s) return;
  if (s.ready) setPill(el.state, "Minecraft ready", "ok");
  else if (s.connected && s.error) setPill(el.state, "Add-on problem", "bad");
  else if (s.connected) setPill(el.state, "Waiting for add-on", "warn");
  else setPill(el.state, "Minecraft offline", "idle");

  if (s.connected && s.error) {
    showNotice("bad", "Add-on handshake failed", [s.error, "Enable or reload the behavior pack. The app retries when the add-on starts again."]);
  } else if (s.connected && !s.ready) {
    showNotice("info", "Waiting for the add-on", ["Minecraft is connected. The add-on answers once it starts. Enable the behavior pack and cheats, or reload it."]);
  } else if (!s.connected && s.disconnect) {
    const d = s.disconnect;
    const lines = [d.message, d.guidance];
    lines.push(`Connect with /wsserver ${location.host}/ws`);
    showNotice("warn", `${REASON_TITLES[d.reason] || d.reason} · ${clock(d.at)}`, lines);
  } else if (!s.connected) {
    showNotice("info", "Minecraft has not connected", [`In Minecraft, run /wsserver ${location.host}/ws.`]);
  } else {
    el.notice.hidden = true;
  }
  renderHealth();
}

function renderHealth() {
  const s = latestState;
  if (!appOnline || !s || !s.connected) {
    el.health.textContent = "";
    el.health.className = "muted";
    return;
  }
  const h = s.health || {};
  const parts = [];
  if (h.latencyMs) parts.push(`heartbeat ${h.latencyMs} ms`);
  if (h.lastSeen) parts.push(`last packet ${ago(h.lastSeen)}`);
  if (h.missedHeartbeats) parts.push(`${h.missedHeartbeats} missed heartbeat${h.missedHeartbeats === 1 ? "" : "s"}`);
  el.health.textContent = parts.join(" · ");
  el.health.className = h.missedHeartbeats ? "warn-text" : "muted";
}

// ---- start ---------------------------------------------------------------

setPill(el.appLink, "Connecting to app…", "warn");
renderHistory();
renderTraffic();
connect();
setInterval(renderHealth, 1000);
