export type Target = "app" | "game";
export type BridgeError = { code: string; message: string };
export type RequestBody = {
  data?: unknown;
  error?: BridgeError;
  durationMs?: number;
};
export type RequestInput = { target: Target; name: string; data: unknown };
export type HistoryItem = {
  at: number;
  target: Target;
  name: string;
  data: string;
  ok: boolean;
  code: string;
  durationMs: number;
  serverMs: number | null;
  body: RequestBody;
};
export type Disconnect = {
  reason: string;
  message: string;
  guidance: string;
  at: number;
};
export type MinecraftState = {
  ready: boolean;
  connected: boolean;
  error?: string;
  disconnect?: Disconnect;
  health?: { latencyMs?: number; lastSeen?: number; missedHeartbeats?: number };
};
export type Frame = {
  kind: string;
  name?: string;
  id?: string;
  error?: unknown;
  [key: string]: unknown;
};
export type LiveMessage =
  | { type: "state"; data: MinecraftState; at: number }
  | { type: "frame"; data: { direction: string; frame: Frame }; at: number }
  | { type: "error"; data: unknown; at: number };
export type TrafficEntry = {
  seq: number;
  at: number;
  type: "frame" | "error" | "notice";
  direction: string;
  kind: string;
  name: string;
  id: string;
  failed: boolean;
  latencyMs: number | null;
  payload: unknown;
  searchText: string;
};
