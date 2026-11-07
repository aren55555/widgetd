// The board's bookkeeping for one stream, apart from the DOM: which cards
// exist and in what order, the cursor each card's view was last handed, and
// whether an incoming delta follows on from it. Pure, so it is tested
// directly.

import type { Cursor as WireCursor, Payload } from "../gen/widgetd/v1/stream_pb.ts";
import type { Cursor } from "@widgetd/sdk/bridge";
import type { Stored } from "./store.ts";

export const toBridgeCursor = (c: WireCursor | undefined): Cursor => ({
  epoch: String(c?.epoch ?? 0n),
  seq: String(c?.seq ?? 0n),
});

export const sameCursor = (a: Cursor | undefined, b: Cursor | undefined) =>
  a !== undefined && b !== undefined && a.epoch === b.epoch && a.seq === b.seq;

/**
 * Whether a delta can be applied by a view last handed `last`. If not, the
 * board has missed something (it fell behind and was disconnected, say) and
 * must reconnect with its stored cursors to be sent what it is missing.
 */
export const deltaFollows = (last: Cursor | undefined, delta: Payload) =>
  sameCursor(last, toBridgeCursor(delta.prev));

/** The stored state a card may start from: only if the same build wrote it. */
export const usableStored = (stored: Stored | undefined, digest: string, stateType: string) =>
  stored && stored.digest === digest && stored.stateType === stateType ? stored : undefined;

/** Grid placement for a card's layout hint, capped to the board's columns. */
export const gridSpan = (cols: number, rows: number, maxCols: number) => ({
  gridColumn: `span ${Math.max(1, Math.min(cols || 1, maxCols))}`,
  gridRow: `span ${Math.max(1, rows || 1)}`,
});

/** Cards a finished initial burst didn't mention: gone while we were away. */
export const staleCards = (cached: Iterable<string>, mounted: ReadonlySet<string>) =>
  [...cached].filter((id) => !mounted.has(id));
