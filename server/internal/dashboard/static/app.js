const target = document.querySelector("#target");
const name = document.querySelector("#name");
const data = document.querySelector("#data");
const result = document.querySelector("#result");
const send = document.querySelector("#send");
const state = document.querySelector("#state");
const frames = document.querySelector("#frames");
const empty = document.querySelector("#empty");
const format = (value) => JSON.stringify(value, null, 2);

target.addEventListener("change", () => {
  name.value = target.value === "app" ? "app.echo" : "game.time";
  data.value = target.value === "app" ? '{"text":"Hello from the dashboard"}' : "null";
});

document.querySelector("#request").addEventListener("submit", async (event) => {
  event.preventDefault();
  send.disabled = true;
  result.textContent = "Waiting for a result…";
  try {
    const payload = { target: target.value, name: name.value.trim(), data: JSON.parse(data.value) };
    const response = await fetch("/api/request", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload),
    });
    result.textContent = format(await response.json());
  } catch (error) {
    result.textContent = error instanceof SyntaxError ? "Enter valid JSON data." : error.message;
  } finally {
    send.disabled = false;
  }
});

document.querySelector("#clear").addEventListener("click", () => {
  frames.replaceChildren();
  empty.hidden = false;
});

function connect() {
  const socket = new WebSocket(`ws://${location.host}/api/live`);
  socket.addEventListener("message", (event) => {
    const message = JSON.parse(event.data);
    if (message.type === "state") {
      const status = message.data;
      state.textContent = status.ready ? "Minecraft ready" : status.error ? `Add-on: ${status.error}` : status.connected ? "Add-on handshake pending" : "App ready · Minecraft offline";
      state.title = status.error || "";
      return;
    }
    empty.hidden = true;
    const entry = document.createElement("li");
    const meta = document.createElement("div");
    meta.className = "meta";
    meta.textContent = `${new Date().toLocaleTimeString()} · ${message.data.direction === "in" ? "Minecraft → App" : message.data.direction === "out" ? "App → Minecraft" : message.type}`;
    const body = document.createElement("pre");
    body.textContent = format(message.data.frame || message.data);
    entry.append(meta, body);
    frames.prepend(entry);
    while (frames.children.length > 100) frames.lastElementChild.remove();
  });
  socket.addEventListener("close", () => {
    state.textContent = "App offline · reconnecting…";
    setTimeout(connect, 1000);
  });
}

connect();
