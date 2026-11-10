// The board's bookkeeping for one stream, apart from the DOM: which cards
// exist and in what order, the cursor each card's view was last handed, and
// whether an incoming delta follows on from it. Pure, so it is tested
// directly.

import { InstanceStatus_State, type Cursor as WireCursor, type Payload } from "../gen/widgetd/v1/stream_pb.ts";
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

/**
 * What a screen shows for an instance in a given state, apart from its
 * cards: nothing while it runs (its cards speak for it), otherwise a
 * placeholder tile saying why, with a way to set it up where that's what
 * it needs.
 */
export type Placeholder = { show: false } | { show: true; text: string; setUp: boolean; signIn?: boolean };

export function placeholderFor(state: InstanceStatus_State, message: string, canSignIn = false): Placeholder {
  switch (state) {
    case InstanceStatus_State.RUNNING:
      return { show: false };
    case InstanceStatus_State.NEEDS_CONFIG:
      return { show: true, text: "Not set up yet.", setUp: true };
    case InstanceStatus_State.INVALID_CONFIG:
      return { show: true, text: "Its settings need updating.", setUp: true };
    case InstanceStatus_State.FAULTED:
      return { show: true, text: message ? `Stopped: ${message}. Retrying…` : "Stopped. Retrying…", setUp: false };
    case InstanceStatus_State.NEEDS_SIGN_IN:
      return { show: true, text: message || "Needs an account signed in.", setUp: false, signIn: canSignIn };
    default:
      return { show: false };
  }
}

/** A card id's instance: the part before the first "/". */
export const instanceOf = (cardId: string) => cardId.slice(0, Math.max(0, cardId.indexOf("/")));
