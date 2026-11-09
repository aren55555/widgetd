// The view-half SDK. A widget's view calls defineWidget once; the SDK then
// runs the card: it receives payloads from the board, checks each one against
// the widget's schema, applies deltas, renders, and hands the accepted state
// back to the board to keep, so the next page load draws instantly and
// resumes from where it left off.

import { create, toBinary, fromBinary, type DescMessage, type Message, type MessageShape } from "@bufbuild/protobuf";
import type { Cursor, FromFrame, Local as LocalBytes, ToFrame } from "./bridge.ts";
import { decodeChecked, typeUrlOf, validated } from "./checks.ts";

export { decodeChecked, validated, typeUrlOf } from "./checks.ts";
export type { Cursor } from "./bridge.ts";

export type WidgetStorage = {
  /** A value this card stored earlier, e.g. a chosen zoom level. */
  get(key: string): Promise<string | undefined>;
  /** Store (or with null, remove) a value. Quota-limited per card. */
  set(key: string, value: string | null): Promise<void>;
};

/**
 * The card's typed local value: a message of the widget's `local` type that
 * this screen keeps for the card (a chosen zoom level, a dismissed hint). It
 * is ready before the first `update`, so the first draw already uses it.
 */
export type Local<M> = {
  /** The saved value, checked against its schema; the type's defaults if none was saved. */
  readonly value: M;
  /**
   * Check and keep `value`, then save it. `this.value` changes at once; the
   * promise settles when the board has stored it, and rejects if `value`
   * breaks a rule or the card's storage quota is used up.
   */
  set(value: M): Promise<void>;
  /** Forget the saved value; `this.value` goes back to the defaults. */
  clear(): Promise<void>;
};

export type WidgetContext<L = never> = {
  cardId: string;
  /** The element to render into. It fills the card. */
  root: HTMLElement;
  /** Untyped string storage. Prefer `local` for anything structured. */
  storage: WidgetStorage;
  /** The typed local value. Only usable when the widget declares a `local` type. */
  local: Local<L>;
  /**
   * Send the widget's server half something the viewer did (a tap, a
   * choice). The message must be the widget's `action` type; it is checked
   * here, and again by the host, before the widget sees it.
   */
  act(action: Message): void;
};

export type View<S, D> = {
  /** Called with every accepted state; `delta` is set when a delta produced it. */
  update(state: S, delta?: D): void;
  destroy?(): void;
};

export type WidgetDef<
  SD extends DescMessage,
  DD extends DescMessage = DescMessage,
  LD extends DescMessage = DescMessage,
> = {
  state: SD;
  delta?: DD;
  /** The message `ctx.local` holds, if the view keeps typed local state. */
  local?: LD;
  /** The message `ctx.act` sends, if the card takes actions. */
  action?: DescMessage;
  /** Pure: the state after `delta`. Required when `delta` is given. */
  apply?: (state: MessageShape<SD>, delta: MessageShape<DD>) => MessageShape<SD>;
  mount(ctx: WidgetContext<MessageShape<LD>>): View<MessageShape<SD>, MessageShape<DD>>;
};

/**
 * The part of a running card that doesn't touch the DOM: it turns board
 * messages into checked states and replies. Kept apart so it can be tested
 * without a browser.
 */
export class CardCore<SD extends DescMessage, DD extends DescMessage, LD extends DescMessage = DescMessage> {
  private state: MessageShape<SD> | undefined;
  private view: View<MessageShape<SD>, MessageShape<DD>> | undefined;
  private localValue: MessageShape<LD> | undefined;
  /** The card's typed local value, as `ctx.local`. */
  readonly local: Local<MessageShape<LD>>;

  /**
   * `persist` asks the board to keep (or with null, forget) the local
   * value; it rejects if the board refuses.
   */
  constructor(
    private def: WidgetDef<SD, DD, LD>,
    private send: (m: FromFrame) => void,
    private makeView: (local: Local<MessageShape<LD>>) => View<MessageShape<SD>, MessageShape<DD>>,
    private persist: (local: LocalBytes | null) => Promise<void> = async () => {},
  ) {
    const core = this;
    const schema = () => {
      if (!def.local) throw new Error("this widget declares no local type");
      return def.local;
    };
    this.local = {
      get value() {
        return (core.localValue ??= create(schema()));
      },
      set(value) {
        try {
          const ld = schema();
          const v = validated(ld, value);
          if (!v.ok) throw new Error(v.error);
          core.localValue = value;
          return core.persist({ type: ld.typeName, value: toBinary(ld, value) });
        } catch (e) {
          return Promise.reject(e);
        }
      },
      clear() {
        try {
          core.localValue = create(schema());
          return core.persist(null);
        } catch (e) {
          return Promise.reject(e);
        }
      },
    };
  }

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
    if (this.def.local) this.localValue = loadLocal(this.def.local, m.local);
    this.view = this.makeView(this.local);
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

/**
 * The saved local value if it is of the declared type, decodes and passes its
 * rules; otherwise the defaults. A value saved as another type (the widget
 * changed its `local` message) is ignored, and replaced on the next set.
 * Unknown fields are allowed, so a value saved by an older or newer build of
 * the same message still loads.
 */
function loadLocal<Desc extends DescMessage>(schema: Desc, saved: LocalBytes | undefined): MessageShape<Desc> {
  if (saved?.type === schema.typeName && saved.value instanceof Uint8Array) {
    try {
      const m = fromBinary(schema, saved.value);
      if (validated(schema, m).ok) return m;
    } catch {
      /* fall through to the defaults */
    }
  }
  return create(schema);
}

/** A deep copy, so `apply` can mutate freely without touching kept state. */
function structuredCloneMessage<Desc extends DescMessage>(schema: Desc, m: MessageShape<Desc>): MessageShape<Desc> {
  return fromBinary(schema, toBinary(schema, m));
}

/** Define the widget's view. Call once, at the top level of view.tsx. */
export function defineWidget<
  SD extends DescMessage,
  DD extends DescMessage = DescMessage,
  LD extends DescMessage = DescMessage,
>(def: WidgetDef<SD, DD, LD>): WidgetDef<SD, DD, LD> {
  if (def.delta && !def.apply) throw new Error("defineWidget: a delta type needs apply");
  if (typeof window !== "undefined" && window.parent !== window) start(def);
  return def;
}

function start<SD extends DescMessage, DD extends DescMessage, LD extends DescMessage>(
  def: WidgetDef<SD, DD, LD>,
): void {
  const parent = window.parent;
  const send = (m: FromFrame) => parent.postMessage(m, "*");
  let nextId = 1;
  const pending = new Map<number, (m: Extract<ToFrame, { t: "storage" }>) => void>();
  const call = (
    m:
      | { t: "storage-get"; key: string }
      | { t: "storage-set"; key: string; value: string | null }
      | { t: "local-set"; local: LocalBytes | null },
  ) =>
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
  const act = (action: Message) => {
    const schema = def.action;
    if (!schema) throw new Error("this widget declares no action type");
    const v = validated(schema, action as never);
    if (!v.ok) throw new Error(v.error);
    send({ t: "action", typeUrl: typeUrlOf(schema), value: toBinary(schema, action as never) });
  };
  const persist = async (local: LocalBytes | null) => {
    const r = await call({ t: "local-set", local });
    if (r.error) throw new Error(r.error);
  };
  const core = new CardCore(def, send, (local) => def.mount({ cardId, root, storage, act, local }), persist);

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
