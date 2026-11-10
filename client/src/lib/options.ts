// A screen's BoardOptions, passed to the board page like function arguments:
// one query parameter, `?o=`, holding the unpadded base64url of the binary
// message. Fields left unset aren't written, so an empty message is no `o`
// at all. A value that doesn't decode or breaks a rule is ignored as a
// whole, so a bad link still shows the default board.

import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { base64Decode, base64Encode } from "@bufbuild/protobuf/wire";

import { BoardOptionsSchema, type BoardOptions } from "../gen/widgetd/v1/board_pb.ts";

export const DEFAULT_COLS = 4;
export const DEFAULT_STALE_AFTER_MS = 30_000;

export type BoardOptionsRead = {
  options: BoardOptions;
  /** Why `o` was ignored, if it was. */
  error?: string;
};

/** The options in a page's query string (`location.search`). */
export function readBoardOptions(search: string): BoardOptionsRead {
  const o = new URLSearchParams(search).get("o");
  if (!o) return { options: create(BoardOptionsSchema) };
  return decodeBoardOptions(o);
}

/** Decode and check one `o` value. */
export function decodeBoardOptions(o: string): BoardOptionsRead {
  const empty = create(BoardOptionsSchema);
  if (!/^[A-Za-z0-9_-]*$/.test(o)) return { options: empty, error: "o is not base64url" };
  let options: BoardOptions;
  try {
    options = fromBinary(BoardOptionsSchema, base64Decode(o));
  } catch (e) {
    return { options: empty, error: `o doesn't decode: ${e instanceof Error ? e.message : String(e)}` };
  }
  // Unknown fields are fine: they come from a newer contract.
  const error = checkRules(options);
  if (error) return { options: empty, error };
  return { options };
}

/**
 * board.proto's buf.validate rules, checked by hand: protovalidate (with its
 * CEL runtime) would more than triple board.js for two rules. options.test.ts
 * checks that this agrees with protovalidate, so the two can't drift.
 */
export function checkRules(o: BoardOptions): string | undefined {
  if (o.cols !== undefined && (o.cols < 1 || o.cols > 12)) return `cols: must be 1 to 12, got ${o.cols}`;
  const sa = o.staleAfter;
  if (sa && sa.seconds < 5n) return "stale_after: must be at least 5s";
  return undefined;
}

/** The `o` value for `opts`; empty when nothing is set. */
export function encodeBoardOptions(opts: BoardOptions): string {
  return base64Encode(toBinary(BoardOptionsSchema, opts), "url");
}

export type LayoutSource = "set" | "attribute" | "default";

export type Layout = {
  cols: number;
  colsSource: LayoutSource;
  staleAfterMs: number;
  staleAfterSource: LayoutSource;
};

/** Layout values, by precedence: the URL, then the element's attribute, then the default. */
export function resolveLayout(opts: BoardOptions, colsAttr: string | null): Layout {
  const attr = Math.floor(Number(colsAttr));
  const [cols, colsSource]: [number, LayoutSource] =
    opts.cols !== undefined ? [opts.cols, "set"] : attr >= 1 ? [attr, "attribute"] : [DEFAULT_COLS, "default"];
  const sa = opts.staleAfter;
  const [staleAfterMs, staleAfterSource]: [number, LayoutSource] = sa
    ? [Number(sa.seconds) * 1000 + Math.floor(sa.nanos / 1e6), "set"]
    : [DEFAULT_STALE_AFTER_MS, "default"];
  return { cols, colsSource, staleAfterMs, staleAfterSource };
}
