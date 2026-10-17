import { BridgeError, decode, encode, failure } from "./protocol.js";
import type { Frame, Json } from "./protocol.js";

export type Handler = (data: Json) => Json | Promise<Json>;
export type Listener = (data: Json) => void;
export type Clock = { set(callback: () => void, milliseconds: number): number; clear(id: number): void };
export type BridgeOptions = {
  // Receives listener failures. The default writes a warning to the console.
  onError?(error: unknown): void;
};

export class Bridge {
  private readonly handlers = new Map<string, Handler>();
  private readonly events = new Map<string, Set<Listener>>();
  private readonly pending = new Map<string, { resolve(data: Json): void; reject(error: unknown): void }>();
  private readonly prefix = `peer-${Date.now()}-${Math.random().toString(36).slice(2)}`;
  private readonly onError: (error: unknown) => void;
  private sequence = 0;
  private active = 0;
  private closed = false;

  constructor(
    private readonly send: (raw: string) => void,
    private readonly clock: Clock,
    options: BridgeOptions = {},
  ) {
    this.onError = options.onError ?? ((error) => console.warn(`Bridge: ${error}`));
    this.handle("bridge.hello", () => ({ protocol: 1 }));
    // Heartbeat: any answer, including UNKNOWN_REQUEST from older peers, shows the peer is alive.
    this.handle("bridge.ping", () => null);
  }

  // Returns a function that removes this handler.
  handle(name: string, handler: Handler): () => void {
    this.ensureOpen();
    if (!name || this.handlers.has(name)) throw new Error(`Invalid or duplicate request handler: ${name}`);
    this.handlers.set(name, handler);
    return () => {
      if (this.handlers.get(name) === handler) this.handlers.delete(name);
    };
  }

  on(name: string, listener: Listener): () => void {
    this.ensureOpen();
    if (!this.events.has(name)) this.events.set(name, new Set());
    const listeners = this.events.get(name)!;
    listeners.add(listener);
    return () => { listeners.delete(listener); };
  }

  request(name: string, data: Json = null, timeoutMs = 10_000): Promise<Json> {
    if (this.closed) return Promise.reject(new BridgeError("DISCONNECTED", "Bridge closed"));
    if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) return Promise.reject(new Error("Timeout must be positive"));
    if (this.pending.size >= 16) return Promise.reject(new BridgeError("BUSY", "Too many pending requests"));
    const id = `${this.prefix}-${++this.sequence}`;
    return new Promise((resolve, reject) => {
      const timer = this.clock.set(() => {
        this.pending.delete(id);
        reject(new BridgeError("TIMEOUT", "Request ended before a result arrived"));
      }, timeoutMs);
      this.pending.set(id, {
        resolve: (data) => { this.clock.clear(timer); resolve(data); },
        reject: (error) => { this.clock.clear(timer); reject(error); },
      });
      try {
        this.send(encode({ v: 1, kind: "request", id, name, data }));
      } catch (error) {
        this.pending.delete(id);
        this.clock.clear(timer);
        reject(error);
      }
    });
  }

  emit(name: string, data: Json = null): void {
    this.ensureOpen();
    this.send(encode({ v: 1, kind: "event", name, data }));
  }

  async receive(raw: string): Promise<void> {
    if (this.closed) return;
    const frame = decode(raw);
    if (frame.kind === "response") {
      const pending = this.pending.get(frame.id);
      this.pending.delete(frame.id);
      if (frame.error) pending?.reject(new BridgeError(frame.error.code, frame.error.message));
      else pending?.resolve(frame.data);
      return;
    }
    if (frame.kind === "event") {
      // Each listener runs alone, so one failure cannot stop the others or the connection.
      for (const listener of [...(this.events.get(frame.name) ?? [])]) {
        try {
          listener(frame.data);
        } catch (error) {
          this.report(frame.name, error);
        }
      }
      return;
    }
    let response: Frame;
    if (this.active >= 16) {
      response = { v: 1, kind: "response", id: frame.id, error: { code: "BUSY", message: "Too many active handlers" } };
    } else {
      this.active++;
      try {
        const handler = this.handlers.get(frame.name);
        if (!handler) throw new BridgeError("UNKNOWN_REQUEST", `Unknown request: ${frame.name}`);
        response = { v: 1, kind: "response", id: frame.id, data: await handler(frame.data) };
      } catch (error) {
        response = { v: 1, kind: "response", id: frame.id, error: failure(error) };
      } finally {
        this.active--;
      }
    }
    if (this.closed) return;
    let encoded;
    try { encoded = encode(response); } catch (error) {
      encoded = encode({ v: 1, kind: "response", id: frame.id, error: failure(error) });
    }
    this.send(encoded);
  }

  close(): void {
    this.closed = true;
    for (const pending of this.pending.values()) pending.reject(new BridgeError("DISCONNECTED", "Bridge closed"));
    this.pending.clear();
    for (const listeners of this.events.values()) listeners.clear();
    this.events.clear();
    this.handlers.clear();
  }

  private report(name: string, error: unknown): void {
    const message = error instanceof Error ? error.message : String(error);
    try {
      this.onError(new BridgeError("LISTENER_ERROR", `Listener for ${name} failed: ${message}`));
    } catch {
      // A failing reporter must not break event delivery.
    }
  }

  private ensureOpen(): void {
    if (this.closed) throw new BridgeError("DISCONNECTED", "Bridge closed");
  }
}
