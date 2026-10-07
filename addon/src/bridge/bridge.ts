import { BridgeError, decode, encode, failure, Frame, Json } from "./protocol";

type Handler = (data: Json) => Json | Promise<Json>;
type Listener = (data: Json) => void;
type Clock = { set(callback: () => void, milliseconds: number): number; clear(id: number): void };

export class Bridge {
  private readonly handlers = new Map<string, Handler>();
  private readonly events = new Map<string, Set<Listener>>();
  private readonly pending = new Map<string, { resolve(data: Json): void; reject(error: unknown): void }>();
  private readonly prefix = `addon-${Date.now()}-${Math.random().toString(36).slice(2)}`;
  private sequence = 0;
  private active = 0;

  constructor(private readonly send: (raw: string) => void, private readonly clock: Clock) {
    this.handle("bridge.hello", () => ({ protocol: 1 }));
  }

  handle(name: string, handler: Handler): void {
    if (!name || this.handlers.has(name)) throw new Error(`Invalid or duplicate request handler: ${name}`);
    this.handlers.set(name, handler);
  }

  on(name: string, listener: Listener): () => void {
    if (!this.events.has(name)) this.events.set(name, new Set());
    this.events.get(name)!.add(listener);
    return () => { this.events.get(name)!.delete(listener); };
  }

  request(name: string, data: Json = null): Promise<Json> {
    if (this.pending.size >= 16) return Promise.reject(new BridgeError("BUSY", "Too many pending requests"));
    const id = `${this.prefix}-${++this.sequence}`;
    return new Promise((resolve, reject) => {
      const timer = this.clock.set(() => {
        this.pending.delete(id);
        reject(new BridgeError("TIMEOUT", "Request ended before a result arrived"));
      }, 10_000);
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
    this.send(encode({ v: 1, kind: "event", name, data }));
  }

  async receive(raw: string): Promise<void> {
    const frame = decode(raw);
    if (frame.kind === "response") {
      const pending = this.pending.get(frame.id);
      this.pending.delete(frame.id);
      if (frame.error) pending?.reject(new BridgeError(frame.error.code, frame.error.message));
      else pending?.resolve(frame.data);
      return;
    }
    if (frame.kind === "event") {
      for (const listener of this.events.get(frame.name) ?? []) listener(frame.data);
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
    let encoded;
    try { encoded = encode(response); } catch (error) {
      encoded = encode({ v: 1, kind: "response", id: frame.id, error: failure(error) });
    }
    this.send(encoded);
  }
}
