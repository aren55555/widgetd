// The view-half SDK. A widget's view calls defineWidget once; the SDK then
// runs the card: it receives payloads from the board, checks each one against
// the widget's schema, applies deltas, renders, and hands the accepted state
// back to the board to keep, so the next page load draws instantly and
// resumes from where it left off.

import { toBinary, fromBinary, type DescMessage, type MessageShape } from "@bufbuild/protobuf";
import type { Cursor, FromFrame, ToFrame } from "./bridge.ts";
import { decodeChecked, validated } from "./checks.ts";

export { decodeChecked, validated, typeUrlOf } from "./checks.ts";
export type { Cursor } from "./bridge.ts";

export type WidgetStorage = {
  /** A value this card stored earlier, e.g. a chosen zoom level. */
  get(key: string): Promise<string | undefined>;
  /** Store (or with null, remove) a value. Quota-limited per card. */
  set(key: string, value: string | null): Promise<void>;
};

export type WidgetContext = {
  cardId: string;
  /** The element to render into. It fills the card. */
  root: HTMLElement;
  storage: WidgetStorage;
};

export type View<S, D> = {
  /** Called with every accepted state; `delta` is set when a delta produced it. */
  update(state: S, delta?: D): void;
  destroy?(): void;
};

export type WidgetDef<SD extends DescMessage, DD extends DescMessage = DescMessage> = {
  state: SD;
  delta?: DD;
  /** Pure: the state after `delta`. Required when `delta` is given. */
  apply?: (state: MessageShape<SD>, delta: MessageShape<DD>) => MessageShape<SD>;
  mount(ctx: WidgetContext): View<MessageShape<SD>, MessageShape<DD>>;
};

/**
 * The part of a running card that doesn't touch the DOM: it turns board
 * messages into checked states and replies. Kept apart so it can be tested
 * without a browser.
 */
export class CardCore<SD extends DescMessage, DD extends DescMessage> {
  private state: MessageShape<SD> | undefined;
  private view: View<MessageShape<SD>, MessageShape<DD>> | undefined;

  constructor(
    private def: WidgetDef<SD, DD>,
    private send: (m: FromFrame) => void,
    private makeView: () => View<MessageShape<SD>, MessageShape<DD>>,
  ) {}

  get current(): MessageShape<SD> | undefined {
    return this.state;
  }

  handle(m: ToFrame): void {
    switch (m.t) {
      case "init":
        return this.init(m);
      case "snapshot": {
        const r = decodeChecked(this.def.state, m.typeUrl, m.value);
        if (!r.ok) return this.fault(r.error, m.cursor);
        return this.accept(r.message, undefined, m.cursor);
      }
      case "delta": {
        if (!this.def.delta || !this.def.apply) return this.fault("this view takes no deltas", m.cursor);
        if (this.state === undefined) return this.fault("delta before any state", m.cursor);
        const r = decodeChecked(this.def.delta, m.typeUrl, m.value);
        if (!r.ok) return this.fault(r.error, m.cursor);
        let next: MessageShape<SD>;
        try {
          next = this.def.apply(structuredCloneMessage(this.def.state, this.state), r.message);
        } catch (e) {
          return this.fault(`apply failed: ${(e as Error).message}`, m.cursor);
        }
        const v = validated(this.def.state, next);
        if (!v.ok) return this.fault(`state after delta: ${v.error}`, m.cursor);
        return this.accept(next, r.message, m.cursor);
      }
    }
  }

  private init(m: Extract<ToFrame, { t: "init" }>): void {
    if (m.stateType !== this.def.state.typeName) {
      return this.fault(`card sends ${m.stateType}, view expects ${this.def.state.typeName}`);
    }
    if ((m.deltaType || undefined) !== this.def.delta?.typeName) {
      return this.fault(`card sends deltas of ${m.deltaType || "nothing"}, view expects ${this.def.delta?.typeName ?? "none"}`);
    }
    this.view = this.makeView();
    if (m.stored) {
      // Stored state was checked when it was accepted, but storage is outside
      // our control, so check it again; if it fails, just wait for the server.
      try {
        const s = fromBinary(this.def.state, m.stored.state);
        if (validated(this.def.state, s).ok) {
          this.state = s;
          this.view.update(s);
        }
      } catch {
        /* ignore: the server's snapshot will replace it */
      }
    }
  }

  private accept(next: MessageShape<SD>, delta: MessageShape<DD> | undefined, cursor: Cursor): void {
    this.state = next;
    this.view?.update(next, delta);
    this.send({ t: "checkpoint", cursor, state: toBinary(this.def.state, next) });
  }

  private fault(message: string, cursor?: Cursor): void {
    this.send({ t: "fault", cursor, message });
  }
}

/** A deep copy, so `apply` can mutate freely without touching kept state. */
function structuredCloneMessage<Desc extends DescMessage>(schema: Desc, m: MessageShape<Desc>): MessageShape<Desc> {
  return fromBinary(schema, toBinary(schema, m));
}

/** Define the widget's view. Call once, at the top level of view.tsx. */
export function defineWidget<SD extends DescMessage, DD extends DescMessage = DescMessage>(
  def: WidgetDef<SD, DD>,
): WidgetDef<SD, DD> {
  if (def.delta && !def.apply) throw new Error("defineWidget: a delta type needs apply");
  if (typeof window !== "undefined" && window.parent !== window) start(def);
  return def;
}

function start<SD extends DescMessage, DD extends DescMessage>(def: WidgetDef<SD, DD>): void {
  const parent = window.parent;
  const send = (m: FromFrame) => parent.postMessage(m, "*");
  let nextId = 1;
  const pending = new Map<number, (m: Extract<ToFrame, { t: "storage" }>) => void>();
  const call = (m: { t: "storage-get"; key: string } | { t: "storage-set"; key: string; value: string | null }) =>
    new Promise<Extract<ToFrame, { t: "storage" }>>((resolve) => {
      const id = nextId++;
      pending.set(id, resolve);
      send({ ...m, id } as FromFrame);
    });

  let cardId = "";
  const root = document.getElementById("root") ?? document.body;
  const storage: WidgetStorage = {
    async get(key) {
      const r = await call({ t: "storage-get", key });
      if (r.error) throw new Error(r.error);
      return r.value;
    },
    async set(key, value) {
      const r = await call({ t: "storage-set", key, value });
      if (r.error) throw new Error(r.error);
    },
  };
  const core = new CardCore(def, send, () => def.mount({ cardId, root, storage }));

  const applyTheme = (theme: Record<string, string>) => {
    for (const [k, v] of Object.entries(theme)) {
      if (k.startsWith("--")) document.documentElement.style.setProperty(k, v);
    }
  };

  window.addEventListener("message", (e: MessageEvent) => {
    if (e.source !== parent) return;
    const m = e.data as ToFrame;
    if (!m || typeof m !== "object") return;
    if (m.t === "storage") {
      pending.get(m.id)?.(m);
      pending.delete(m.id);
      return;
    }
    if (m.t === "theme") return applyTheme(m.theme);
    if (m.t === "init") {
      cardId = m.cardId;
      applyTheme(m.theme);
    }
    core.handle(m);
  });
  send({ t: "ready" });
}
