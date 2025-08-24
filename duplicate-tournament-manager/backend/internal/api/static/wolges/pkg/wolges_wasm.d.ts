/* tslint:disable */
/* eslint-disable */
export function generate_moves(kwg: SpanishKWG, board_json: string, rack: string): string;
export class SpanishKWG {
  free(): void;
  constructor(kwg_bytes: Uint8Array);
  check_word(word: string): boolean;
  find_anagrams(letters: string, max_results: number): string;
}

export type InitInput = RequestInfo | URL | Response | BufferSource | WebAssembly.Module;

export interface InitOutput {
  readonly memory: WebAssembly.Memory;
  readonly __wbg_spanishkwg_free: (a: number, b: number) => void;
  readonly spanishkwg_new: (a: number, b: number) => number;
  readonly spanishkwg_check_word: (a: number, b: number, c: number) => number;
  readonly spanishkwg_find_anagrams: (a: number, b: number, c: number, d: number) => [number, number];
  readonly generate_moves: (a: number, b: number, c: number, d: number, e: number) => [number, number];
  readonly __wbindgen_free: (a: number, b: number, c: number) => void;
  readonly __wbindgen_malloc: (a: number, b: number) => number;
  readonly __wbindgen_realloc: (a: number, b: number, c: number, d: number) => number;
  readonly __wbindgen_export_3: WebAssembly.Table;
  readonly __wbindgen_start: () => void;
}

export type SyncInitInput = BufferSource | WebAssembly.Module;
/**
* Instantiates the given `module`, which can either be bytes or
* a precompiled `WebAssembly.Module`.
*
* @param {{ module: SyncInitInput }} module - Passing `SyncInitInput` directly is deprecated.
*
* @returns {InitOutput}
*/
export function initSync(module: { module: SyncInitInput } | SyncInitInput): InitOutput;

/**
* If `module_or_path` is {RequestInfo} or {URL}, makes a request and
* for everything else, calls `WebAssembly.instantiate` directly.
*
* @param {{ module_or_path: InitInput | Promise<InitInput> }} module_or_path - Passing `InitInput` directly is deprecated.
*
* @returns {Promise<InitOutput>}
*/
export default function __wbg_init (module_or_path?: { module_or_path: InitInput | Promise<InitInput> } | InitInput | Promise<InitInput>): Promise<InitOutput>;
