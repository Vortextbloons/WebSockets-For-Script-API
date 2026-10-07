export type Json = null | boolean | number | string | Json[] | { [key: string]: Json };

export class BridgeError extends Error {
  constructor(public readonly code: string, message: string) {
    super(message);
  }
}

export type Frame =
  | { v: 1; kind: "request"; id: string; name: string; data: Json }
  | { v: 1; kind: "event"; name: string; data: Json }
  | { v: 1; kind: "response"; id: string; data: Json; error?: never }
  | { v: 1; kind: "response"; id: string; error: { code: string; message: string }; data?: never };

export function byteLength(raw: string): number {
  let bytes = 0;
  for (const char of raw) {
    const code = char.codePointAt(0)!;
    bytes += code <= 0x7f ? 1 : code <= 0x7ff ? 2 : code <= 0xffff ? 3 : 4;
  }
  return bytes;
}

export function decode(raw: string): Frame {
  if (byteLength(raw) > 2048) throw new BridgeError("PAYLOAD_TOO_LARGE", "Message exceeds 2048 UTF-8 bytes");
  let frame;
  try { frame = JSON.parse(raw); } catch { throw new BridgeError("INVALID_FRAME", "Invalid JSON"); }
  const text = (value: unknown): value is string => typeof value === "string" && value.length > 0;
  if (!frame || frame.v !== 1) throw new BridgeError("INVALID_FRAME", "Invalid bridge message or version");
  const own = (key: string) => Object.prototype.hasOwnProperty.call(frame, key);
  const id = text(frame.id) && byteLength(frame.id) <= 128;
  const data = own("data");
  const error = own("error");
  const valid = frame.kind === "request" ? id && text(frame.name) && data && !error
    : frame.kind === "event" ? !own("id") && text(frame.name) && data && !error
    : frame.kind === "response" && id && !own("name") && data !== error
      && (!error || (frame.error && text(frame.error.code) && text(frame.error.message)));
  if (!valid) throw new BridgeError("INVALID_FRAME", "Invalid bridge envelope");
  return frame as Frame;
}

export function encode(frame: Frame): string {
  let raw: string;
  try { raw = JSON.stringify(frame); } catch { throw new BridgeError("INVALID_DATA", "Data must be JSON"); }
  decode(raw);
  return raw;
}

export function failure(error: unknown): { code: string; message: string } {
  return {
    code: error instanceof BridgeError ? error.code : "HANDLER_ERROR",
    message: error instanceof Error ? error.message || "Handler failed" : String(error),
  };
}
