import type { Bridge } from "./bridge.js";
import { BridgeError } from "./protocol.js";
import type { Json } from "./protocol.js";
import type { Infer, Schema } from "./schema.js";

export type RequestSpec = { request: Schema<unknown>; response: Schema<unknown> };
export type Contract = {
  requests?: Record<string, RequestSpec>;
  events?: Record<string, Schema<unknown>>;
};

// Declares the request names, event names, and payload schemas one side of the bridge uses.
export function defineContract<C extends Contract>(contract: C): C {
  return contract;
}

type RequestTable<C extends Contract> = NonNullable<C["requests"]>;
type EventTable<C extends Contract> = NonNullable<C["events"]>;
export type RequestName<C extends Contract> = keyof RequestTable<C> & string;
export type EventName<C extends Contract> = keyof EventTable<C> & string;
export type RequestInput<C extends Contract, N extends RequestName<C>> = Infer<RequestTable<C>[N]["request"]>;
export type RequestOutput<C extends Contract, N extends RequestName<C>> = Infer<RequestTable<C>[N]["response"]>;
export type EventPayload<C extends Contract, N extends EventName<C>> = Infer<EventTable<C>[N]>;

export type TypedBridge<C extends Contract> = {
  readonly bridge: Bridge;
  // Handles a request. Input is validated before the handler runs; output is validated before it is sent.
  handle<N extends RequestName<C>>(
    name: N,
    handler: (data: RequestInput<C, N>) => RequestOutput<C, N> | Promise<RequestOutput<C, N>>,
  ): () => void;
  // Sends a request and validates the reply. Invalid input is rejected before anything is sent.
  request<N extends RequestName<C>>(name: N, data: RequestInput<C, N>, timeoutMs?: number): Promise<RequestOutput<C, N>>;
  // Subscribes to an event. Payloads that fail validation are reported through the bridge's onError.
  on<N extends EventName<C>>(name: N, listener: (data: EventPayload<C, N>) => void): () => void;
  emit<N extends EventName<C>>(name: N, data: EventPayload<C, N>): void;
};

export function withContract<C extends Contract>(bridge: Bridge, contract: C): TypedBridge<C> {
  const requests: Record<string, RequestSpec> = contract.requests ?? {};
  const events: Record<string, Schema<unknown>> = contract.events ?? {};
  const requestSpec = (name: string): RequestSpec => {
    const spec = Object.prototype.hasOwnProperty.call(requests, name) ? requests[name] : undefined;
    if (!spec) throw new Error(`Request is not in the contract: ${name}`);
    return spec;
  };
  const eventSchema = (name: string): Schema<unknown> => {
    const schema = Object.prototype.hasOwnProperty.call(events, name) ? events[name] : undefined;
    if (!schema) throw new Error(`Event is not in the contract: ${name}`);
    return schema;
  };

  return {
    bridge,
    handle(name, handler) {
      const spec = requestSpec(name);
      return bridge.handle(name, async (data): Promise<Json> => {
        const input = spec.request.parse(data);
        const output = await handler(input as never);
        try {
          return spec.response.parse(output) as Json;
        } catch (error) {
          // The peer sent valid input; the bad output is this side's bug, so report it as a handler error.
          const reason = error instanceof Error ? error.message : String(error);
          throw new BridgeError("HANDLER_ERROR", `Response for ${name} does not match its schema: ${reason}`);
        }
      });
    },
    request(name, data, timeoutMs) {
      const spec = requestSpec(name);
      let payload: Json;
      try {
        payload = spec.request.parse(data) as Json;
      } catch (error) {
        return Promise.reject(error);
      }
      return bridge.request(name, payload, timeoutMs).then((reply) => spec.response.parse(reply) as never);
    },
    on(name, listener) {
      const schema = eventSchema(name);
      return bridge.on(name, (data) => listener(schema.parse(data) as never));
    },
    emit(name, data) {
      bridge.emit(name, eventSchema(name).parse(data) as Json);
    },
  };
}
