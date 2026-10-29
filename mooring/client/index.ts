import type { Hello, Versions } from "../protocol/generated/ts/types";

/** Thin host-supplied WASM binding; it cannot issue durable saved receipts. */
export interface WasmBindings {
  negotiate_json(hello: string): string;
  TextClient: new () => {
    insert(opId: string, blockId: string, utf16Index: number, text: string): Uint8Array;
    text(blockId: string): string;
    snapshot(): Uint8Array;
    recover(snapshot: Uint8Array, reason: string): void;
    unsaved_json(): string;
    free(): void;
  };
}

export function negotiate(bindings: WasmBindings, hello: Hello): Versions {
  return JSON.parse(bindings.negotiate_json(JSON.stringify(hello))) as Versions;
}
