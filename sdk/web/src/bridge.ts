// The postMessage protocol between a board and the card frames it hosts.
//
// A card frame is a sandboxed iframe in an opaque origin: it has no network,
// no cookies and no storage of its own. Everything it gets, it gets from the
// board through these messages, and everything it keeps, it keeps by asking
// the board. Payload bytes cross unparsed; the frame decodes and checks them
// against its own schema, since only the widget's code has that schema.

/** A card cursor. Strings, because epoch and seq are uint64. */
export type Cursor = { epoch: string; seq: string };

/** Board → frame. */
export type ToFrame =
  | {
      t: "init";
      cardId: string;
      stateType: string;
      deltaType: string;
      theme: Record<string, string>;
      /** The last state this frame accepted, from the board's storage. */
      stored?: { state: Uint8Array; cursor: Cursor };
    }
  | { t: "snapshot"; typeUrl: string; value: Uint8Array; cursor: Cursor }
  | { t: "delta"; typeUrl: string; value: Uint8Array; cursor: Cursor }
  | { t: "theme"; theme: Record<string, string> }
  | { t: "storage"; id: number; value?: string; error?: string };

/** Frame → board. */
export type FromFrame =
  | { t: "ready" }
  /** The frame accepted the payload at `cursor`; `state` is the result. */
  | { t: "checkpoint"; cursor: Cursor; state: Uint8Array }
  /** The frame refused the payload at `cursor` and kept its last good state. */
  | { t: "fault"; cursor?: Cursor; message: string }
  | { t: "storage-get"; id: number; key: string }
  | { t: "storage-set"; id: number; key: string; value: string | null }
  /** Something the viewer did, for the widget's server half: the card's action type. */
  | { t: "action"; typeUrl: string; value: Uint8Array };

export const isFromFrame = (v: unknown): v is FromFrame =>
  typeof v === "object" && v !== null && typeof (v as { t?: unknown }).t === "string";

/**
 * Theme tokens a board passes into every card. A card frame can't inherit
 * the page's CSS, so the board reads these custom properties from itself
 * (where the embedding page sets them) and sets them on the frame's root.
 * Widgets style against them, with their own fallbacks.
 */
export const THEME_TOKENS = [
  "--wd-bg",
  "--wd-surface",
  "--wd-raised",
  "--wd-edge",
  "--wd-ink",
  "--wd-ink-soft",
  "--wd-ink-muted",
  "--wd-ink-faint",
  "--wd-accent",
  "--wd-up",
  "--wd-down",
  "--wd-warn",
  "--wd-font-sans",
  "--wd-font-mono",
  "--wd-radius",
] as const;
