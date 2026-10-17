<script lang="ts">
  import { onMount } from "svelte";
  import type {
    Frame,
    HistoryItem,
    LiveMessage,
    MinecraftState,
    RequestBody,
    Target,
    TrafficEntry,
  } from "./types";

  const MAX_TRAFFIC = 500,
    MAX_VISIBLE = 300,
    MAX_HISTORY = 50,
    MAX_ROUND_TRIPS = 100;
  const HEARTBEAT = "bridge.ping",
    HISTORY_KEY = "script-api-bridge.history";
  const REASON_TITLES: Record<string, string> = {
    MINECRAFT_CLOSED: "Minecraft closed the connection",
    CONNECTION_LOST: "Connection to Minecraft was lost",
    MESSAGE_TOO_LARGE: "Minecraft sent a message that was too large",
    WRITE_FAILED: "Commands could not be sent to Minecraft",
    HEARTBEAT_TIMEOUT: "Minecraft stopped answering the add-on",
    SERVER_STOPPED: "The server stopped the connection",
  };
  type Token = { text: string; className?: string };
  type EntryInput = {
    type: TrafficEntry["type"];
    at?: number;
    direction?: string;
    frame?: Frame;
    payload?: unknown;
  };
  let target = $state<Target>("app");
  let name = $state("app.echo");
  let dataText = $state('{"text":"Hello from the dashboard"}');
  let jsonError = $state("");
  let busy = $state(false);
  let result = $state<RequestBody | null>(null);
  let resultMessage = $state("Send a request to see its result.");
  let resultMeta = $state("");
  let copyLabel = $state("Copy");
  let history = $state<HistoryItem[]>(loadHistory());
  let latestState = $state<MinecraftState | null>(null);
  let appOnline = $state(false);
  let connectedBefore = false;
  let attempts = 0;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  let healthTimer: ReturnType<typeof setInterval> | undefined;
  const timeouts = new Set<ReturnType<typeof setTimeout>>();
  let socket: WebSocket | undefined;
  let traffic = $state<TrafficEntry[]>([]);
  let sequence = 0;
  let search = $state("");
  let direction = $state("all");
  let kind = $state("all");
  let hideHeartbeats = $state(false);
  let exportFormat = $state("json");
  let expanded = $state(new Set<number>());
  let openRequests = new Map<string, TrafficEntry>();
  let roundTrips: number[] = $state([]);

  function clock(ms: number) {
    return new Date(ms).toLocaleTimeString();
  }
  function schedule(callback: () => void, delay: number) {
    const timer = setTimeout(() => {
      timeouts.delete(timer);
      callback();
    }, delay);
    timeouts.add(timer);
    return timer;
  }
  function ago(ms: number) {
    const seconds = Math.max(0, Math.round((Date.now() - ms) / 1000));
    return seconds < 60
      ? `${seconds}s ago`
      : `${Math.round(seconds / 60)}m ago`;
  }
  function loadHistory(): HistoryItem[] {
    try {
      const saved: unknown = JSON.parse(
        localStorage.getItem(HISTORY_KEY) || "[]",
      );
      return Array.isArray(saved)
        ? (saved.slice(0, MAX_HISTORY) as HistoryItem[])
        : [];
    } catch {
      return [];
    }
  }
  function saveHistory() {
    try {
      localStorage.setItem(HISTORY_KEY, JSON.stringify(history));
    } catch {}
  }
  function parseStored(text: string): unknown {
    try {
      return JSON.parse(text);
    } catch {
      return null;
    }
  }
  function tokens(value: unknown, depth = 0): Token[] {
    const out: Token[] = [],
      indent = "  ".repeat(depth),
      inner = "  ".repeat(depth + 1);
    const add = (text: string, className?: string) =>
      out.push({ text, className });
    if (value === null) add("null", "j-null");
    else if (typeof value === "string") add(JSON.stringify(value), "j-str");
    else if (typeof value === "number") add(String(value), "j-num");
    else if (typeof value === "boolean") add(String(value), "j-bool");
    else if (Array.isArray(value)) {
      if (!value.length) add("[]");
      else {
        add("[\n");
        value.forEach((item, i) => {
          add(inner);
          out.push(...tokens(item, depth + 1));
          add(i < value.length - 1 ? ",\n" : "\n");
        });
        add(`${indent}]`);
      }
    } else if (typeof value === "object") {
      const keys = Object.keys(value);
      if (!keys.length) add("{}");
      else {
        add("{\n");
        keys.forEach((key, i) => {
          add(inner);
          add(JSON.stringify(key), "j-key");
          add(": ");
          out.push(
            ...tokens((value as Record<string, unknown>)[key], depth + 1),
          );
          add(i < keys.length - 1 ? ",\n" : "\n");
        });
        add(`${indent}}`);
      }
    }
    return out;
  }
  function addHistory(item: HistoryItem) {
    history = [item, ...history].slice(0, MAX_HISTORY);
    saveHistory();
  }
  function storedResult(item: HistoryItem) {
    result = item.body;
    resultMessage = "";
    resultMeta = `${item.ok ? "OK" : item.code} · ${item.durationMs} ms round trip${item.serverMs === null ? "" : ` · ${item.serverMs} ms in server`} · ${clock(item.at)}`;
  }
  function selectHistory(item: HistoryItem) {
    target = item.target;
    name = item.name;
    dataText = item.data;
    jsonError = "";
    storedResult(item);
  }
  function addEntry(input: EntryInput) {
    const entry: TrafficEntry = {
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
      searchText: "",
    };
    if (input.type === "frame" && input.frame) {
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
    entry.searchText =
      `${entry.kind} ${entry.name} ${entry.id} ${entry.direction} ${JSON.stringify(entry.payload)}`.toLowerCase();
    traffic = [...traffic, entry].slice(-MAX_TRAFFIC);
    if (traffic.length === MAX_TRAFFIC) {
      const retained = new Set(traffic.map((row) => row.seq));
      expanded = new Set([...expanded].filter((id) => retained.has(id)));
    }
  }
  function pairFrame(entry: TrafficEntry) {
    const frame = entry.payload as Frame;
    if (!frame.id) return;
    if (frame.kind === "request") {
      openRequests.set(frame.id, entry);
      if (openRequests.size > 200)
        openRequests.delete(openRequests.keys().next().value!);
      return;
    }
    if (frame.kind !== "response") return;
    const request = openRequests.get(frame.id);
    if (!request) return;
    openRequests.delete(frame.id);
    entry.name = request.name;
    entry.latencyMs = Math.max(0, entry.at - request.at);
    request.latencyMs = entry.latencyMs;
    roundTrips = [...roundTrips, entry.latencyMs].slice(-MAX_ROUND_TRIPS);
  }
  function receive(message: LiveMessage) {
    if (message.type === "state") latestState = message.data;
    else if (message.type === "frame")
      addEntry({
        type: "frame",
        at: message.at,
        direction: message.data.direction,
        frame: message.data.frame,
      });
    else if (message.type === "error")
      addEntry({ type: "error", at: message.at, payload: message.data });
  }
  function connect() {
    if (!active) return;
    const protocol = location.protocol === "https:" ? "wss:" : "ws:";
    const current = new WebSocket(`${protocol}//${location.host}/api/live`);
    socket = current;
    current.addEventListener("open", () => {
      if (!active) return;
      attempts = 0;
      appOnline = true;
      if (connectedBefore)
        addEntry({
          type: "notice",
          payload:
            "Reconnected to the app. Traffic while the dashboard was disconnected was not recorded.",
        });
      connectedBefore = true;
    });
    current.addEventListener("message", (event: MessageEvent<string>) => {
      try {
        receive(JSON.parse(event.data) as LiveMessage);
      } catch {}
    });
    current.addEventListener("close", () => {
      if (socket !== current || !active) return;
      appOnline = false;
      attempts += 1;
      const delay = Math.min(10_000, 1_000 * 2 ** Math.min(attempts - 1, 3));
      retryLabel = `App offline · retrying in ${delay / 1000}s`;
      retryTimer = setTimeout(connect, delay);
    });
  }
  let active = false;
  let retryLabel = $state("Connecting to app…");
  onMount(() => {
    active = true;
    connect();
    healthTimer = setInterval(() => {
      healthTick = Date.now();
    }, 1000);
    return () => {
      active = false;
      clearTimeout(retryTimer);
      clearInterval(healthTimer);
      for (const timer of timeouts) clearTimeout(timer);
      timeouts.clear();
      socket?.close();
    };
  });
  let healthTick = $state(Date.now());
  let health = $derived.by(() => {
    void healthTick;
    const s = latestState;
    if (!appOnline || !s || !s.connected) return "";
    const h = s.health || {},
      parts: string[] = [];
    if (h.latencyMs) parts.push(`heartbeat ${h.latencyMs} ms`);
    if (h.lastSeen) parts.push(`last packet ${ago(h.lastSeen)}`);
    if (h.missedHeartbeats)
      parts.push(
        `${h.missedHeartbeats} missed heartbeat${h.missedHeartbeats === 1 ? "" : "s"}`,
      );
    return parts.join(" · ");
  });
  let stateLabel = $derived(
    !appOnline
      ? "Minecraft unknown"
      : !latestState
        ? "Minecraft unknown"
        : latestState.ready
          ? "Minecraft ready"
          : latestState.connected && latestState.error
            ? "Add-on problem"
            : latestState.connected
              ? "Waiting for add-on"
              : "Minecraft offline",
  );
  let stateTone = $derived(
    !appOnline || !latestState || (!latestState.connected && !latestState.ready)
      ? "idle"
      : latestState.error
        ? "bad"
        : latestState.ready
          ? "ok"
          : "warn",
  );
  let notice = $derived.by(() => {
    const s = latestState;
    if (!appOnline)
      return {
        tone: "warn",
        title: "The app is offline",
        lines: [
          "Minecraft status is unknown until the dashboard reconnects. Start the app if it has stopped.",
        ],
      };
    if (!s) return null;
    if (s.connected && s.error)
      return {
        tone: "bad",
        title: "Add-on handshake failed",
        lines: [
          s.error,
          "Enable or reload the behavior pack. The app retries when the add-on starts again.",
        ],
      };
    if (s.connected && !s.ready)
      return {
        tone: "info",
        title: "Waiting for the add-on",
        lines: [
          "Minecraft is connected. The add-on answers once it starts. Enable the behavior pack and cheats, or reload it.",
        ],
      };
    if (!s.connected && s.disconnect)
      return {
        tone: "warn",
        title: `${REASON_TITLES[s.disconnect.reason] || s.disconnect.reason} · ${clock(s.disconnect.at)}`,
        lines: [
          s.disconnect.message,
          s.disconnect.guidance,
          `Connect with /wsserver ${location.hostname}:8765/ws`,
        ],
      };
    if (!s.connected)
      return {
        tone: "info",
        title: "Minecraft has not connected",
        lines: ["In Minecraft, run /wsserver localhost:8765/ws."],
      };
    return null;
  });
  let visible = $derived(
    traffic
      .filter(
        (entry) =>
          (direction === "all" || entry.direction === direction) &&
          (kind === "all" ||
            (kind === "error" ? entry.failed : entry.kind === kind)) &&
          !(hideHeartbeats && entry.name === HEARTBEAT) &&
          (!search.trim() ||
            entry.searchText.includes(search.trim().toLowerCase())),
      )
      .slice(-MAX_VISIBLE)
      .reverse(),
  );
  let avg = $derived(
    roundTrips.length
      ? Math.round(
          roundTrips.reduce((sum, val) => sum + val, 0) / roundTrips.length,
        )
      : null,
  );
  let max = $derived(roundTrips.length ? Math.max(...roundTrips) : null);
  function directionLabel(entry: TrafficEntry) {
    return entry.direction === "in"
      ? "Minecraft → App"
      : entry.direction === "out"
        ? "App → Minecraft"
        : entry.type === "notice"
          ? "Dashboard"
          : "Minecraft";
  }
  function setExpanded(id: number, value: boolean) {
    const next = new Set(expanded);
    value ? next.add(id) : next.delete(id);
    expanded = next;
  }
  async function sendRequest(
    requestTarget = target,
    requestName = name.trim(),
    requestData?: unknown,
    originalText = dataText,
  ) {
    let data = requestData;
    if (arguments.length < 3) {
      try {
        data = JSON.parse(dataText);
      } catch (error) {
        jsonError = `Invalid JSON: ${(error as Error).message}`;
        return;
      }
    }
    jsonError = "";
    busy = true;
    result = null;
    resultMessage = "Waiting for a result…";
    resultMeta = "";
    const started = performance.now(),
      at = Date.now();
    let body: RequestBody;
    try {
      const response = await fetch("/api/request", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          target: requestTarget,
          name: requestName,
          data,
        }),
      });
      body = (await response.json()) as RequestBody;
    } catch (error) {
      body = {
        error: {
          code: "NETWORK",
          message: `Dashboard server unreachable: ${(error as Error).message}`,
        },
      };
    } finally {
      busy = false;
    }
    const item: HistoryItem = {
      at,
      target: requestTarget,
      name: requestName,
      data: originalText,
      ok: !body.error,
      code: body.error?.code || "",
      durationMs: Math.round(performance.now() - started),
      serverMs: typeof body.durationMs === "number" ? body.durationMs : null,
      body,
    };
    addHistory(item);
    storedResult(item);
  }
  function changeTarget() {
    name = target === "app" ? "app.echo" : "game.time";
    dataText =
      target === "app" ? '{"text":"Hello from the dashboard"}' : "null";
    jsonError = "";
  }
  function formatJSON() {
    try {
      dataText = JSON.stringify(JSON.parse(dataText), null, 2);
      jsonError = "";
    } catch (error) {
      jsonError = `Invalid JSON: ${(error as Error).message}`;
    }
  }
  async function copyResult() {
    if (!result) return;
    try {
      await navigator.clipboard.writeText(JSON.stringify(result, null, 2));
      copyLabel = "Copied";
    } catch {
      copyLabel = "Copy failed";
    }
    schedule(() => (copyLabel = "Copy"), 1200);
  }
  function download(filename: string, content: string, type: string) {
    const url = URL.createObjectURL(new Blob([content], { type })),
      link = document.createElement("a");
    link.href = url;
    link.download = filename;
    link.click();
    schedule(() => URL.revokeObjectURL(url), 1000);
  }
  function exportTraffic() {
    const rows = [...traffic]
      .filter(
        (entry) =>
          (direction === "all" || entry.direction === direction) &&
          (kind === "all" ||
            (kind === "error" ? entry.failed : entry.kind === kind)) &&
          !(hideHeartbeats && entry.name === HEARTBEAT) &&
          (!search.trim() ||
            entry.searchText.includes(search.trim().toLowerCase())),
      )
      .map((entry) => ({
        time: new Date(entry.at).toISOString(),
        direction: entry.direction || null,
        kind: entry.kind,
        name: entry.name || null,
        id: entry.id || null,
        failed: entry.failed,
        latencyMs: entry.latencyMs,
        payload: entry.payload,
      }));
    const stamp = new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
    if (exportFormat === "ndjson") {
      download(
        `script-api-traffic-${stamp}.ndjson`,
        rows.map((row) => JSON.stringify(row)).join("\n") + "\n",
        "application/x-ndjson",
      );
      return;
    }
    const report = {
      exportedAt: new Date().toISOString(),
      filters: { search, direction, kind, hideHeartbeats },
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
    download(
      `script-api-traffic-${stamp}.json`,
      JSON.stringify(report, null, 2),
      "application/json",
    );
  }
  function clearTraffic() {
    traffic = [];
    openRequests.clear();
    roundTrips = [];
    expanded = new Set();
  }
</script>

<svelte:head><title>Script API Bridge</title></svelte:head>
<header>
  <div>
    <h1>Script API Bridge</h1>
    <p>Minecraft ⇄ Your app</p>
  </div>
  <div class="status">
    <span class="pill" class:ok={appOnline} class:bad={!appOnline} role="status"
      >{appOnline ? "App live" : retryLabel}</span
    ><span
      class="pill"
      class:ok={stateTone === "ok"}
      class:warn={stateTone === "warn"}
      class:bad={stateTone === "bad"}
      class:idle={stateTone === "idle"}
      role="status">{stateLabel}</span
    ><span
      class:warn-text={!!latestState?.health?.missedHeartbeats}
      class="muted">{health}</span
    >
  </div>
</header>
{#if notice}<div
    class="notice"
    class:warn={notice.tone === "warn"}
    class:bad={notice.tone === "bad"}
    class:info={notice.tone === "info"}
  >
    <strong>{notice.title}</strong>
    <div>
      {#each notice.lines as line}<p>{line}</p>{/each}
    </div>
  </div>{/if}
<main>
  <section aria-labelledby="request-title">
    <h2 id="request-title">Send a request</h2>
    <form
      onsubmit={(event) => {
        event.preventDefault();
        void sendRequest();
      }}
    >
      <label for="target">Destination</label><select
        id="target"
        bind:value={target}
        onchange={changeTarget}
        ><option value="app">Local app</option><option value="game"
          >Minecraft</option
        ></select
      >
      <label for="name">Request name</label><input
        id="name"
        bind:value={name}
        required
        spellcheck="false"
      />
      <label for="data">JSON data</label><textarea
        id="data"
        rows="7"
        bind:value={dataText}
        spellcheck="false"></textarea>
      {#if jsonError}<p class="error">{jsonError}</p>{/if}
      <div class="row">
        <button type="button" class="ghost" onclick={formatJSON}
          >Format JSON</button
        ><button type="submit" disabled={busy}>Send request</button>
      </div>
    </form>
    <div class="section-head">
      <h2>Result</h2>
      <button type="button" class="ghost small" onclick={copyResult}
        >{copyLabel}</button
      >
    </div>
    <p id="result-meta" class="muted">{resultMeta}</p>
    <pre
      class="json"
      aria-live="polite">{#if result}{#each tokens(result) as token}<span
            class={token.className}>{token.text}</span
          >{/each}{:else}{resultMessage}{/if}</pre>
    <h2>Minecraft round trips</h2>
    <dl class="stats">
      <div>
        <dt>Last</dt>
        <dd>{roundTrips.length ? `${roundTrips.at(-1)} ms` : "–"}</dd>
      </div>
      <div>
        <dt>Average</dt>
        <dd>{avg === null ? "–" : `${avg} ms`}</dd>
      </div>
      <div>
        <dt>Slowest</dt>
        <dd>{max === null ? "–" : `${max} ms`}</dd>
      </div>
      <div>
        <dt>Samples</dt>
        <dd>{roundTrips.length}</dd>
      </div>
    </dl>
    <div class="section-head">
      <h2>Request history</h2>
      <button
        type="button"
        class="ghost small"
        onclick={() => {
          history = [];
          saveHistory();
        }}>Clear</button
      >
    </div>
    {#if history.length === 0}<p class="muted">
        Requests you send appear here, with their results and timing.
      </p>{/if}
    <ol class="history">
      {#each history as item}<li class="history-item" class:fail={!item.ok}>
          <button
            type="button"
            class="history-open"
            onclick={() => selectHistory(item)}
            ><span class="history-name">{item.target} · {item.name}</span><span
              class="muted"
              >{item.ok ? "OK" : item.code} · {item.durationMs} ms · {clock(
                item.at,
              )}</span
            ></button
          ><button
            type="button"
            class="ghost small"
            onclick={() =>
              void sendRequest(
                item.target,
                item.name,
                parseStored(item.data),
                item.data,
              )}>Run again</button
          >
        </li>{/each}
    </ol>
    <details>
      <summary>Connect Minecraft</summary>
      <p>Enable the behavior pack and cheats, then run:</p>
      <code>/wsserver localhost:8765/ws</code>
      <p>Test the opposite direction:</p>
      <code>/scriptevent scaffold:demo Hello</code>
    </details>
  </section>
  <section class="traffic" aria-labelledby="traffic-title">
    <div class="section-head">
      <h2 id="traffic-title">Live traffic</h2>
      <div class="actions">
        <select bind:value={exportFormat} aria-label="Export format"
          ><option value="json">JSON</option><option value="ndjson"
            >NDJSON</option
          ></select
        ><button type="button" class="ghost small" onclick={exportTraffic}
          >Export</button
        ><button type="button" class="ghost small" onclick={clearTraffic}
          >Clear</button
        >
      </div>
    </div>
    <div class="filters" role="search">
      <input
        type="search"
        bind:value={search}
        placeholder="Search names, IDs, or data"
        spellcheck="false"
        aria-label="Search traffic"
      /><select bind:value={direction} aria-label="Direction"
        ><option value="all">All directions</option><option value="in"
          >Minecraft → App</option
        ><option value="out">App → Minecraft</option></select
      ><select bind:value={kind} aria-label="Kind"
        ><option value="all">All kinds</option><option value="request"
          >Requests</option
        ><option value="response">Responses</option><option value="event"
          >Events</option
        ><option value="error">Errors</option></select
      ><label class="check"
        ><input type="checkbox" bind:checked={hideHeartbeats} /> Hide heartbeats</label
      >
    </div>
    <p class="muted counts" aria-live="polite">
      {traffic.filter(
        (entry) =>
          (direction === "all" || entry.direction === direction) &&
          (kind === "all" ||
            (kind === "error" ? entry.failed : entry.kind === kind)) &&
          !(hideHeartbeats && entry.name === HEARTBEAT) &&
          (!search.trim() ||
            entry.searchText.includes(search.trim().toLowerCase())),
      ).length} of {traffic.length} entries{visible.length >= MAX_VISIBLE &&
      traffic.length > visible.length
        ? ` · showing newest ${MAX_VISIBLE}`
        : ""}
    </p>
    {#if traffic.length === 0}<p class="empty">
        Game requests, responses, and events appear here once Minecraft
        connects.
      </p>{/if}
    <ol class="entries">
      {#each visible as entry (entry.seq)}<li
          class="entry"
          class:failed={entry.failed}
          class:dir-in={entry.direction === "in"}
          class:dir-out={entry.direction === "out"}
        >
          <div class="entry-head">
            <span class="time">{clock(entry.at)}</span><span
              class="badge"
              class:kind-error={entry.kind === "error"}
              class:kind-event={entry.kind === "event"}
              class:kind-request={entry.kind === "request"}
              class:kind-response={entry.kind === "response"}
              class:kind-notice={entry.kind === "notice"}>{entry.kind}</span
            ><span class="direction">{directionLabel(entry)}</span><span
              class="entry-name">{entry.name || entry.id}</span
            >{#if entry.latencyMs !== null}<span class="latency"
                >{entry.latencyMs} ms</span
              >{/if}
          </div>
          <details
            open={expanded.has(entry.seq)}
            ontoggle={(event) =>
              setExpanded(entry.seq, event.currentTarget.open)}
          >
            <summary>Payload</summary>
            <pre class="json">{#if entry.type === "notice"}{String(
                  entry.payload,
                )}{:else}{#each tokens(entry.payload) as token}<span
                    class={token.className}>{token.text}</span
                  >{/each}{/if}</pre>
          </details>
        </li>{/each}
    </ol>
  </section>
</main>
