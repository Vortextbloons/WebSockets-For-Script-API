import { BridgeError } from "./protocol.js";

// A schema checks JSON from a peer and returns the typed value.
// Failures throw INVALID_DATA with the path to the bad field.
export interface Schema<T> {
  parse(value: unknown, path?: string): T;
}

export interface OptionalSchema<T> extends Schema<T | undefined> {
  readonly optional: true;
}

export type Infer<S> = S extends Schema<infer T> ? T : never;

type OptionalKeys<S> = { [K in keyof S]: S[K] extends { readonly optional: true } ? K : never }[keyof S];
type RequiredKeys<S> = Exclude<keyof S, OptionalKeys<S>>;
export type ObjectOutput<S> = { [K in RequiredKeys<S>]: Infer<S[K]> } & { [K in OptionalKeys<S>]?: Infer<S[K]> };

function at(path: string): string {
  return path ? ` at ${path}` : "";
}

function fail(expected: string, path: string): BridgeError {
  return new BridgeError("INVALID_DATA", `Expected ${expected}${at(path)}`);
}

function schemaFor<T>(parse: (value: unknown, path: string) => T): Schema<T> {
  return { parse: (value, path = "") => parse(value, path) };
}

// Builds schemas for JSON payloads. Object output contains only the declared fields,
// so unknown fields from newer peers are dropped rather than rejected.
export const schema = {
  string: (): Schema<string> => schemaFor((value, path) => {
    if (typeof value !== "string") throw fail("string", path);
    return value;
  }),
  number: (): Schema<number> => schemaFor((value, path) => {
    if (typeof value !== "number" || !Number.isFinite(value)) throw fail("number", path);
    return value;
  }),
  boolean: (): Schema<boolean> => schemaFor((value, path) => {
    if (typeof value !== "boolean") throw fail("boolean", path);
    return value;
  }),
  null: (): Schema<null> => schemaFor((value, path) => {
    if (value !== null) throw fail("null", path);
    return null;
  }),
  literal: <T extends string | number | boolean>(expected: T): Schema<T> => schemaFor((value, path) => {
    if (value !== expected) throw fail(JSON.stringify(expected), path);
    return expected;
  }),
  array: <T>(item: Schema<T>): Schema<T[]> => schemaFor((value, path) => {
    if (!Array.isArray(value)) throw fail("array", path);
    return value.map((entry, index) => item.parse(entry, `${path}[${index}]`));
  }),
  object: <S extends Record<string, Schema<unknown>>>(shape: S): Schema<ObjectOutput<S>> => schemaFor((value, path) => {
    if (typeof value !== "object" || value === null || Array.isArray(value)) throw fail("object", path);
    const input = value as Record<string, unknown>;
    const output: Record<string, unknown> = {};
    for (const key of Object.keys(shape)) {
      const field = shape[key];
      const fieldPath = path ? `${path}.${key}` : key;
      if (!Object.prototype.hasOwnProperty.call(input, key) || input[key] === undefined) {
        if ((field as Partial<OptionalSchema<unknown>>).optional) continue;
        throw new BridgeError("INVALID_DATA", `Missing ${fieldPath}`);
      }
      output[key] = field.parse(input[key], fieldPath);
    }
    return output as ObjectOutput<S>;
  }),
  // Use inside schema.object for fields that may be absent.
  optional: <T>(item: Schema<T>): OptionalSchema<T> => ({
    optional: true,
    parse: (value, path = "") => (value === undefined ? undefined : item.parse(value, path)),
  }),
};
