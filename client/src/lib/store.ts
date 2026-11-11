// What a screen keeps between visits, in localStorage: for each card, the
// last state its view accepted and the cursor it was accepted at (so the
// next visit draws instantly and asks the server only for what changed), and
// a small space the view can use for its own preferences: string keys, and
// one typed local value kept as its type name next to its bytes. Also, for the
// whole screen, when it last heard from the server (the offline badge).
//
// Each card's entries are namespaced by card id. Its state is tagged with the
// widget build's digest and state type, so a new build of a widget never
// reads state written by an old one. Preferences outlive builds: the view
// checks the local value's type name and ignores a value of another type.

import type { Cursor, Local } from "@widgetd/sdk/bridge";

export interface Stored {
  digest: string;
  stateType: string;
  cursor: Cursor;
  state: Uint8Array;
}

/** The subset of the Web Storage API the store needs, so tests can fake it. */
export type KVBackend = Pick<Storage, "getItem" | "setItem" | "removeItem" | "key" | "length">;

/** Characters a card's preferences (keys, values and local value) may hold. */
export const VIEW_QUOTA = 64 * 1024;

/** How often, at most, `markLive` writes. */
export const LIVE_EVERY = 10_000;

const b64 = (b: Uint8Array) => {
  let s = "";
  for (const x of b) s += String.fromCharCode(x);
  return btoa(s);
};
const unb64 = (s: string) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));

export class CardStore {
  constructor(
    private ls: KVBackend,
    private prefix = "widgetd:",
  ) {}

  private stateKey = (cardId: string) => `${this.prefix}state:${cardId}`;
  private viewPrefix = (cardId: string) => `${this.prefix}view:${cardId}:`;
  private localKey = (cardId: string) => `${this.prefix}local:${cardId}`;

  load(cardId: string): Stored | undefined {
    const raw = this.ls.getItem(this.stateKey(cardId));
    if (!raw) return undefined;
    try {
      // Parsed from storage: anything could be there, so check before trusting.
      const v = JSON.parse(raw) as { digest?: unknown; stateType: string; cursor?: Cursor; state: string };
      if (typeof v.digest !== "string" || typeof v.cursor?.seq !== "string") return undefined;
      return { digest: v.digest, stateType: v.stateType, cursor: v.cursor, state: unb64(v.state) };
    } catch {
      return undefined;
    }
  }

  /** Best effort: a full localStorage only means the next visit resyncs. */
  save(cardId: string, s: Stored): void {
    try {
      this.ls.setItem(
        this.stateKey(cardId),
        JSON.stringify({ digest: s.digest, stateType: s.stateType, cursor: s.cursor, state: b64(s.state) }),
      );
    } catch {
      /* quota exceeded */
    }
  }

  /** Every card with stored state, for the stream's resume request. */
  saved(): Array<{ cardId: string; stored: Stored }> {
    const p = `${this.prefix}state:`;
    const out: Array<{ cardId: string; stored: Stored }> = [];
    for (const k of this.keys()) {
      if (!k.startsWith(p)) continue;
      const cardId = k.slice(p.length);
      const stored = this.load(cardId);
      if (stored) out.push({ cardId, stored });
    }
    return out;
  }

  /** Forget a card's state; with `view`, its preferences too. */
  forget(cardId: string, view: boolean): void {
    this.ls.removeItem(this.stateKey(cardId));
    if (!view) return;
    for (const k of this.keys()) if (k.startsWith(this.viewPrefix(cardId))) this.ls.removeItem(k);
    this.ls.removeItem(this.localKey(cardId));
  }

  /** The card's typed local value, if it saved one. */
  localGet(cardId: string): Local | undefined {
    const raw = this.ls.getItem(this.localKey(cardId));
    if (!raw) return undefined;
    try {
      const v = JSON.parse(raw) as { type: unknown; value: unknown };
      if (typeof v.type !== "string" || typeof v.value !== "string") return undefined;
      return { type: v.type, value: unb64(v.value) };
    } catch {
      return undefined;
    }
  }

  /** Keep (or with null, forget) the card's typed local value. It counts towards the card's quota. */
  localSet(cardId: string, local: Local | null): void {
    const k = this.localKey(cardId);
    if (local === null) {
      this.ls.removeItem(k);
      return;
    }
    const raw = JSON.stringify({ type: local.type, value: b64(local.value) });
    if (this.used(cardId, k) + raw.length > VIEW_QUOTA)
      throw new Error(`storage quota of ${VIEW_QUOTA} bytes exceeded`);
    this.ls.setItem(k, raw);
  }

  /** What the card's preferences take up, leaving out the entry at `except`. */
  private used(cardId: string, except: string): number {
    const p = this.viewPrefix(cardId);
    const local = this.localKey(cardId);
    let used = 0;
    for (const k of this.keys()) {
      if (k === except) continue;
      if (k.startsWith(p)) used += k.length - p.length + (this.ls.getItem(k)?.length ?? 0);
      else if (k === local) used += this.ls.getItem(k)?.length ?? 0;
    }
    return used;
  }

  viewGet(cardId: string, key: string): string | undefined {
    return this.ls.getItem(this.viewPrefix(cardId) + key) ?? undefined;
  }

  viewSet(cardId: string, key: string, value: string | null): void {
    const k = this.viewPrefix(cardId) + key;
    if (value === null) {
      this.ls.removeItem(k);
      return;
    }
    if (this.used(cardId, k) + key.length + value.length > VIEW_QUOTA)
      throw new Error(`storage quota of ${VIEW_QUOTA} bytes exceeded`);
    this.ls.setItem(k, value);
  }

  /** When this screen last heard from the server (ms since the epoch), as of the last write. */
  lastLive(): number | undefined {
    const v = Number(this.ls.getItem(this.liveKey));
    return v > 0 ? v : undefined;
  }

  /**
   * Note that the stream is live at `now`. Written at most once every
   * LIVE_EVERY ms, so keepalives don't wear on storage; the offline badge
   * only needs to be about right.
   */
  markLive(now: number): void {
    if (now - this.liveWritten < LIVE_EVERY) return;
    this.liveWritten = now;
    try {
      this.ls.setItem(this.liveKey, String(now));
    } catch {
      /* quota exceeded */
    }
  }

  private liveWritten = -Infinity;
  private get liveKey() {
    return `${this.prefix}lastLive`;
  }

  private keys(): string[] {
    const out: string[] = [];
    for (let i = 0; i < this.ls.length; i++) {
      const k = this.ls.key(i);
      if (k !== null) out.push(k);
    }
    return out;
  }
}
