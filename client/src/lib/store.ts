// What a screen keeps between visits, in localStorage: for each card, the
// last state its view accepted and the cursor it was accepted at (so the
// next visit draws instantly and asks the server only for what changed), and
// a small key-value space the view can use for its own preferences.
//
// Each card's entries are namespaced by card id and tagged with the widget
// build's digest and state type, so a new build of a widget never reads
// state written by an old one.

import type { Cursor } from "@widgetd/sdk/bridge";

export type Stored = {
  digest: string;
  stateType: string;
  cursor: Cursor;
  state: Uint8Array;
};

/** The subset of the Web Storage API the store needs, so tests can fake it. */
export type KVBackend = Pick<Storage, "getItem" | "setItem" | "removeItem" | "key" | "length">;

/** Bytes a card's own key-value space may hold. */
export const VIEW_QUOTA = 64 * 1024;

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

  load(cardId: string): Stored | undefined {
    const raw = this.ls.getItem(this.stateKey(cardId));
    if (!raw) return undefined;
    try {
      const v = JSON.parse(raw) as { digest: string; stateType: string; cursor: Cursor; state: string };
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
  saved(): { cardId: string; stored: Stored }[] {
    const p = `${this.prefix}state:`;
    const out: { cardId: string; stored: Stored }[] = [];
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
    let used = key.length + value.length;
    for (const other of this.keys()) {
      if (other.startsWith(this.viewPrefix(cardId)) && other !== k) {
        used += other.length - this.viewPrefix(cardId).length + (this.ls.getItem(other)?.length ?? 0);
      }
    }
    if (used > VIEW_QUOTA) throw new Error(`storage quota of ${VIEW_QUOTA} bytes exceeded`);
    this.ls.setItem(k, value);
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
