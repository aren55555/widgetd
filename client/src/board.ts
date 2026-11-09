// <widget-board>: what a screen embeds. It opens the one stream, hosts each
// card in a sandboxed iframe, relays checked-by-the-view payloads between
// them, and keeps each card's accepted state so the next visit draws
// instantly and resumes. An unpaired screen shows a pairing QR code instead.
//
//   <script type="module" src="/widgetd/board.js"></script>
//   <widget-board></widget-board>
//
// Attributes:
//   api    origin of the widgetd server (default: this page's origin)
//   cols   maximum grid columns (default 4)
//
// Theme: set any of THEME_TOKENS as CSS custom properties on the element.

import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { create } from "@bufbuild/protobuf";
import { renderSVG } from "uqr";
import { THEME_TOKENS, type FromFrame, type ToFrame } from "@widgetd/sdk/bridge";

import { AuthService, PairingState, Role } from "./gen/widgetd/v1/auth_pb.ts";
import { WidgetAdminService } from "./gen/widgetd/v1/admin_pb.ts";
import {
  ResumeSchema,
  WidgetStreamService,
  type InstanceStatus,
  type Mounted,
  type Payload,
  type WidgetFrame,
} from "./gen/widgetd/v1/stream_pb.ts";
import { CardStore } from "./lib/store.ts";
import { deltaFollows, gridSpan, instanceOf, placeholderFor, staleCards, toBridgeCursor, usableStored } from "./lib/sync.ts";
import type { Cursor } from "@widgetd/sdk/bridge";

const STYLE = `
:host { display: block; font-family: var(--wd-font-sans, system-ui, sans-serif); color: var(--wd-ink, #e5e7eb); }
.grid { display: grid; gap: 12px; grid-template-columns: repeat(var(--cols), minmax(0, 1fr)); grid-auto-rows: 180px; }
.card { position: relative; display: flex; flex-direction: column; min-width: 0; border-radius: var(--wd-radius, 10px);
  border: 1px solid var(--wd-edge, #27272a); background: var(--wd-surface, #18181b); overflow: hidden; }
.card h3 { margin: 0; padding: 8px 12px 0; font: 500 12px/16px var(--wd-font-mono, ui-monospace, monospace);
  color: var(--wd-ink-muted, #a1a1aa); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.card iframe { flex: 1; width: 100%; border: 0; background: transparent; color-scheme: normal; }
.fault { position: absolute; right: 8px; top: 6px; padding: 1px 6px; border-radius: 999px; font: 11px/16px var(--wd-font-mono, monospace);
  background: color-mix(in oklab, var(--wd-down, #ef4444) 20%, transparent); color: var(--wd-down, #ef4444); cursor: help; }
.notice { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 32px 16px; text-align: center;
  border: 1px dashed var(--wd-edge, #27272a); border-radius: var(--wd-radius, 10px); color: var(--wd-ink-muted, #a1a1aa);
  font: 14px/20px var(--wd-font-mono, ui-monospace, monospace); }
.qr { width: 200px; height: 200px; padding: 10px; background: #fff; border-radius: 8px; }
.qr svg { width: 100%; height: 100%; display: block; }
.code { font: 600 22px/28px var(--wd-font-mono, ui-monospace, monospace); letter-spacing: 0.1em; color: var(--wd-ink, #e5e7eb); }
.status { font-size: 12px; color: var(--wd-ink-faint, #71717a); }
.gear { position: absolute; right: 6px; top: 4px; z-index: 2; padding: 0 4px; border: 0; background: none; cursor: pointer;
  font-size: 14px; line-height: 20px; color: var(--wd-ink-faint, #71717a); opacity: 0; transition: opacity 0.15s; }
.card:hover .gear, .card:focus-within .gear { opacity: 1; }
@media (hover: none) { .gear { opacity: 0.6; } }
.card.placeholder, .card.add { align-items: center; justify-content: center; gap: 8px; padding: 12px; text-align: center; border-style: dashed; }
.card.placeholder p { margin: 0; font: 13px/18px var(--wd-font-mono, ui-monospace, monospace); color: var(--wd-ink-muted, #a1a1aa); }
.action { padding: 6px 12px; border-radius: 8px; border: 1px solid var(--wd-edge, #27272a); cursor: pointer;
  background: var(--wd-raised, #27272a); color: var(--wd-ink, #e5e7eb); font: 13px/18px var(--wd-font-mono, ui-monospace, monospace); }
.link { border: 0; background: none; cursor: pointer; color: var(--wd-ink-faint, #71717a); font: 12px var(--wd-font-mono, monospace); }
.picker { display: flex; flex-wrap: wrap; justify-content: center; gap: 6px; }
/* A set-up or sign-in code, filling its tile however the tile is shaped. The
   overlay is the size container; .fit lays out inside it (a container query
   can't restyle the container itself). The QR is sized from the tile's own
   dimensions: above the text, as big as the text leaves room for, or beside
   it in a tile at least twice as wide as it is tall. */
.overlay { position: absolute; inset: 0; z-index: 3; container-type: size; background: var(--wd-surface, #18181b); }
.overlay .fit { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 6px;
  padding: 8px 26px 8px 8px; box-sizing: border-box; text-align: center; font: 12px/15px var(--wd-font-mono, monospace); color: var(--wd-ink-muted, #a1a1aa); }
.overlay .qr { flex: none; box-sizing: border-box; padding: 4px; border-radius: 6px;
  width: max(64px, min(100cqw - 40px, 100cqh - 72px)); height: max(64px, min(100cqw - 40px, 100cqh - 72px)); }
.overlay .info { display: flex; flex-direction: column; align-items: center; gap: 2px; min-width: 0; }
.overlay .code { font-size: clamp(16px, 10cqmin, 32px); line-height: 1.15; white-space: nowrap; }
@container (min-aspect-ratio: 2 / 1) {
  .overlay .fit { flex-direction: row; gap: 12px; }
  .overlay .qr { width: calc(100cqh - 16px); height: calc(100cqh - 16px); }
  .overlay .info { flex: 1; align-items: flex-start; text-align: left; }
  .overlay .code { font-size: clamp(16px, 16cqh, 36px); }
}
.overlay .close { position: absolute; right: 6px; top: 4px; border: 0; background: none; cursor: pointer; font-size: 18px; color: var(--wd-ink-faint, #71717a); }
`;

const RECONNECT_MIN = 1000;
const RECONNECT_MAX = 30_000;

/** One card: its iframe, what it has been handed, and what waits for it. */
type CardHost = {
  id: string;
  mounted: Mounted;
  el: HTMLElement;
  frame: HTMLIFrameElement;
  title: HTMLElement;
  fault: HTMLElement;
  ready: boolean;
  queue: ToFrame[];
  /** The cursor of the last payload handed to the view. */
  last?: Cursor;
};

class WidgetBoard extends HTMLElement {
  private root = this.attachShadow({ mode: "open" });
  private body = document.createElement("div");
  private grid = document.createElement("div");
  private store = new CardStore(localStorage);
  private cards = new Map<string, CardHost>();
  private instances = new Map<string, { status: InstanceStatus; tile?: HTMLElement }>();
  private addTile = document.createElement("section");
  /** An instance this screen just added: show its set-up code as soon as it appears. */
  private justAdded = "";
  private abort?: AbortController;
  private stopped = true;

  connectedCallback() {
    const style = document.createElement("style");
    style.textContent = STYLE;
    this.grid.className = "grid";
    this.root.replaceChildren(style, this.body);
    window.addEventListener("message", this.onMessage);
    this.stopped = false;
    void this.start();
  }

  disconnectedCallback() {
    this.stopped = true;
    this.abort?.abort();
    window.removeEventListener("message", this.onMessage);
  }

  private get api() {
    return this.getAttribute("api") || location.origin;
  }
  private get maxCols() {
    return Math.max(1, Number(this.getAttribute("cols")) || 4);
  }
  private transport = () => createConnectTransport({ baseUrl: this.api, useBinaryFormat: true });

  private notice(...children: (Node | string)[]) {
    const n = document.createElement("div");
    n.className = "notice";
    n.append(...children);
    this.body.replaceChildren(n);
  }

  private async start() {
    const auth = createClient(AuthService, this.transport());
    let delay = RECONNECT_MIN;
    while (!this.stopped) {
      try {
        const session = await auth.getSession({});
        if (session.role === Role.UNSPECIFIED) {
          await this.pair();
          continue;
        }
        await this.stream();
        delay = RECONNECT_MIN;
      } catch (e) {
        if (this.stopped) return;
        if (e instanceof ConnectError && e.code === Code.Unauthenticated) continue; // re-check: show pairing
        this.setStatus(`reconnectingâ¦ (${e instanceof Error ? e.message : String(e)})`);
        await sleep(delay);
        delay = Math.min(delay * 2, RECONNECT_MAX);
      }
    }
  }

  private setStatus(text: string) {
    let s = this.root.querySelector(".status");
    if (!s) {
      s = document.createElement("div");
      s.className = "status";
      this.body.append(s);
    }
    s.textContent = text;
  }

  /** Show a QR code until an admin approves this screen, then collect the session. */
  private async pair() {
    const auth = createClient(AuthService, this.transport());
    const p = await auth.beginPairing({});
    const qr = document.createElement("div");
    qr.className = "qr";
    qr.innerHTML = renderSVG(p.approveUrl, { border: 1 });
    const code = document.createElement("div");
    code.className = "code";
    code.textContent = p.code;
    this.notice("This screen isn't paired yet.", qr, "Scan with a phone signed in as an admin, and check it shows:", code);

    this.abort = new AbortController();
    for await (const r of auth.awaitPairing({ pairingId: p.pairingId, secret: p.secret }, { signal: this.abort.signal })) {
      if (r.state === PairingState.APPROVED) {
        await auth.completePairing({ pairingId: p.pairingId, secret: p.secret });
        return;
      }
      if (r.state === PairingState.EXPIRED) return; // loop starts a fresh pairing
    }
  }

  private async stream() {
    const client = createClient(WidgetStreamService, this.transport());
    const resume = this.store.saved().map(({ cardId, stored }) =>
      create(ResumeSchema, {
        cardId,
        digest: stored.digest,
        cursor: { epoch: BigInt(stored.cursor.epoch), seq: BigInt(stored.cursor.seq) },
      }),
    );
    // Cards and instances from a previous connection are rebuilt from the burst.
    this.cards.clear();
    this.instances.clear();
    this.grid.replaceChildren();
    this.resetAddTile();
    this.grid.style.setProperty("--cols", String(this.maxCols));

    this.abort = new AbortController();
    const mountedInBurst = new Set<string>();
    let synced = false;
    for await (const msg of client.streamWidgets({ resume }, { signal: this.abort.signal })) {
      const f = msg.frame;
      if (!f) {
        if (!synced) {
          synced = true;
          for (const id of staleCards(resume.map((r) => r.cardId), mountedInBurst)) this.store.forget(id, true);
          this.render();
        }
        continue;
      }
      if (f.body.case === "mounted") mountedInBurst.add(f.cardId);
      if (!this.apply(f)) {
        // Out of sync: reconnect, resuming from what the views accepted.
        this.abort.abort();
        return;
      }
      if (synced) this.render();
    }
  }

  private render() {
    if (this.grid.parentNode !== this.body) this.body.replaceChildren(this.grid);
    this.grid.append(this.addTile); // always last
  }

  /** Handle one frame; false means the board missed something and must resync. */
  private apply(f: WidgetFrame): boolean {
    switch (f.body.case) {
      case "mounted":
        this.mount(f.cardId, f.body.value);
        return true;
      case "unmounted": {
        const c = this.cards.get(f.cardId);
        c?.el.remove();
        this.cards.delete(f.cardId);
        if (f.body.value.purge) this.store.forget(f.cardId, true);
        return true;
      }
      case "snapshot":
        return this.forward(f.cardId, "snapshot", f.body.value);
      case "delta": {
        const c = this.cards.get(f.cardId);
        if (!c || !deltaFollows(c.last, f.body.value)) return false;
        return this.forward(f.cardId, "delta", f.body.value);
      }
      case "fault":
        this.showFault(f.cardId, f.body.value.message);
        return true;
      case "instance":
        this.instanceStatus(f.instanceId, f.body.value);
        return true;
      default:
        return true;
    }
  }

  private mount(id: string, m: Mounted) {
    const existing = this.cards.get(id);
    if (existing && existing.mounted.frameUrl === m.frameUrl) {
      existing.mounted = m;
      existing.title.textContent = m.title;
      Object.assign(existing.el.style, gridSpan(m.layout?.cols ?? 1, m.layout?.rows ?? 1, this.maxCols));
      return;
    }
    existing?.el.remove();

    const el = document.createElement("section");
    el.className = "card";
    const title = document.createElement("h3");
    title.textContent = m.title;
    const fault = document.createElement("span");
    fault.className = "fault";
    fault.hidden = true;
    const frame = document.createElement("iframe");
    // Scripts only: no allow-same-origin, so the card runs in an opaque
    // origin, apart from this page, its cookies and its storage.
    frame.setAttribute("sandbox", "allow-scripts");
    frame.setAttribute("referrerpolicy", "no-referrer");
    frame.setAttribute("loading", "eager");
    frame.title = m.title || id;
    frame.src = new URL(m.frameUrl, this.api).href;
    const gear = document.createElement("button");
    gear.className = "gear";
    gear.title = "Settings";
    gear.setAttribute("aria-label", "Settings");
    gear.textContent = "\u2699";
    gear.addEventListener("click", () => void this.showSetUp(el, instanceOf(id)));
    if (m.title) el.append(title);
    el.append(gear, fault, frame);
    Object.assign(el.style, gridSpan(m.layout?.cols ?? 1, m.layout?.rows ?? 1, this.maxCols));

    const stored = usableStored(this.store.load(id), m.widget?.digest ?? "", m.stateType);
    const card: CardHost = { id, mounted: m, el, frame, title, fault, ready: false, queue: [], last: stored?.cursor };
    this.cards.set(id, card);
    this.grid.append(el);
  }

  // ---- instances, placeholders, set-up codes ----

  private instanceStatus(id: string, st: InstanceStatus) {
    const known = this.instances.get(id);
    if (st.removed) {
      known?.tile?.remove();
      this.instances.delete(id);
      return;
    }
    const entry = known ?? { status: st };
    entry.status = st;
    this.instances.set(id, entry);
    const p = placeholderFor(st.state, st.message);
    if (!p.show) {
      entry.tile?.remove();
      entry.tile = undefined;
      return;
    }
    const tile = entry.tile ?? document.createElement("section");
    tile.className = "card placeholder";
    const title = document.createElement("h3");
    title.textContent = st.title;
    const text = document.createElement("p");
    text.textContent = p.text;
    tile.replaceChildren(title, text);
    if (p.setUp) {
      const btn = document.createElement("button");
      btn.className = "action";
      btn.textContent = "Set up";
      btn.addEventListener("click", () => void this.showSetUp(tile, id));
      tile.append(btn);
    }
    if (p.signIn) {
      const btn = document.createElement("button");
      btn.className = "action";
      btn.textContent = "Sign in";
      btn.addEventListener("click", () => void this.showSignIn(tile, id));
      tile.append(btn);
    }
    if (!entry.tile) {
      entry.tile = tile;
      this.grid.append(tile);
      this.grid.append(this.addTile);
    }
    if (this.justAdded === id) {
      this.justAdded = "";
      void this.showSetUp(tile, id);
    }
  }

  /**
   * Cover `within` with a QR code that opens this instance's settings on a
   * phone. The code is good for one use and ten minutes; the phone still
   * needs an admin passkey to save anything.
   */
  private async showSetUp(within: HTMLElement, instanceId: string) {
    const o = this.codeOverlay(within);
    try {
      const admin = createClient(WidgetAdminService, this.transport());
      const t = await admin.requestConfigTicket({ instanceId });
      o.show(t.url, ["Scan with your phone to set this up."]);
      o.expireAt(t.expiresAt ? Number(t.expiresAt.seconds) * 1000 : Date.now() + 600_000);
    } catch (e) {
      o.fail(`Couldn't get a code: ${e instanceof Error ? e.message : String(e)}`);
    }
  }

  /**
   * Cover `within` with a device-flow sign-in: the code to enter, where, and
   * a QR code that opens that page on a phone (with the code filled in, if
   * the provider supports it). The host waits for the approval; the tile
   * turns into the widget when it comes.
   */
  private async showSignIn(within: HTMLElement, instanceId: string) {
    const o = this.codeOverlay(within);
    try {
      const admin = createClient(WidgetAdminService, this.transport());
      const s = await admin.beginSignIn({ instanceId });
      o.show(s.verificationUriComplete || s.verificationUri, [`Sign in at ${s.verificationUri.replace(/^https:\/\//, "")}`], s.userCode);
      o.expireAt(s.expiresAt ? Number(s.expiresAt.seconds) * 1000 : Date.now() + 600_000);
    } catch (e) {
      o.fail(`Couldn't start sign-in: ${e instanceof Error ? e.message : String(e)}`);
    }
  }

  /**
   * An overlay filling `within` that shows a QR code with a line or two of
   * text (and, for sign-in, the code to type), laid out to fit the tile:
   * see .overlay in STYLE.
   */
  private codeOverlay(within: HTMLElement) {
    within.querySelector(".overlay")?.remove();
    const overlay = document.createElement("div");
    overlay.className = "overlay";
    const close = document.createElement("button");
    close.className = "close";
    close.setAttribute("aria-label", "Close");
    close.textContent = "×";
    close.addEventListener("click", () => overlay.remove());
    const fit = document.createElement("div");
    fit.className = "fit";
    fit.textContent = "Getting a code…";
    overlay.append(close, fit);
    within.append(overlay);
    return {
      show(url: string, lines: string[], code?: string) {
        const qr = document.createElement("div");
        qr.className = "qr";
        qr.innerHTML = renderSVG(url, { border: 1 });
        const info = document.createElement("div");
        info.className = "info";
        for (const l of lines) {
          const p = document.createElement("div");
          p.textContent = l;
          info.append(p);
        }
        if (code) {
          const c = document.createElement("div");
          c.className = "code";
          c.textContent = code;
          info.append(c);
        }
        fit.replaceChildren(qr, info);
      },
      fail(message: string) {
        fit.textContent = message;
      },
      expireAt(ms: number) {
        setTimeout(() => overlay.remove(), Math.max(1000, ms - Date.now()));
      },
    };
  }

  /** The last tile: add a widget from this screen. */
  private resetAddTile() {
    const t = this.addTile;
    t.className = "card add";
    const btn = document.createElement("button");
    btn.className = "action";
    btn.textContent = "+ Add widget";
    btn.addEventListener("click", () => void this.pickWidget());
    t.replaceChildren(btn);
  }

  private async pickWidget() {
    const t = this.addTile;
    const admin = createClient(WidgetAdminService, this.transport());
    try {
      const { widgets } = await admin.listWidgets({});
      const list = document.createElement("div");
      list.className = "picker";
      for (const w of widgets) {
        const b = document.createElement("button");
        b.className = "action";
        b.textContent = w.title;
        b.addEventListener("click", async () => {
          const { instanceId } = await admin.addInstance({ widget: w.name });
          this.justAdded = instanceId;
          const known = this.instances.get(instanceId);
          if (known?.tile) void this.showSetUp(known.tile, instanceId);
          this.resetAddTile();
        });
        list.append(b);
      }
      const cancel = document.createElement("button");
      cancel.className = "link";
      cancel.textContent = "Cancel";
      cancel.addEventListener("click", () => this.resetAddTile());
      t.replaceChildren(list, cancel);
    } catch (e) {
      t.replaceChildren(`Couldn't list widgets: ${e instanceof Error ? e.message : String(e)}`);
      setTimeout(() => this.resetAddTile(), 4000);
    }
  }

  private forward(id: string, t: "snapshot" | "delta", p: Payload): boolean {
    const c = this.cards.get(id);
    if (!c) return true;
    const cursor = toBridgeCursor(p.cursor);
    c.last = cursor;
    this.post(c, { t, typeUrl: p.typeUrl, value: p.value, cursor });
    return true;
  }

  private post(c: CardHost, m: ToFrame) {
    if (!c.ready) c.queue.push(m);
    else c.frame.contentWindow?.postMessage(m, "*");
  }

  private theme(): Record<string, string> {
    const cs = getComputedStyle(this);
    const out: Record<string, string> = {};
    for (const t of THEME_TOKENS) {
      const v = cs.getPropertyValue(t).trim();
      if (v) out[t] = v;
    }
    return out;
  }

  private showFault(id: string, message: string | undefined) {
    const c = this.cards.get(id);
    if (!c) return;
    c.fault.hidden = !message;
    c.fault.textContent = "data error";
    c.fault.title = message ?? "";
  }

  private onMessage = (e: MessageEvent) => {
    let card: CardHost | undefined;
    for (const c of this.cards.values()) if (c.frame.contentWindow === e.source) card = c;
    if (!card) return;
    const m = e.data as FromFrame;
    if (!m || typeof m !== "object") return;
    const reply = (r: ToFrame) => card.frame.contentWindow?.postMessage(r, "*");

    switch (m.t) {
      case "ready": {
        if (card.ready) {
          // The frame reloaded and lost its state. Resync everything from
          // what the views last accepted rather than guess what it missed.
          this.abort?.abort();
          return;
        }
        const stored = usableStored(this.store.load(card.id), card.mounted.widget?.digest ?? "", card.mounted.stateType);
        card.ready = true;
        reply({
          t: "init",
          cardId: card.id,
          stateType: card.mounted.stateType,
          deltaType: card.mounted.deltaType,
          theme: this.theme(),
          stored: stored && { state: stored.state, cursor: stored.cursor },
          local: this.store.localGet(card.id),
        });
        for (const q of card.queue.splice(0)) reply(q);
        return;
      }
      case "checkpoint":
        if (m.state instanceof Uint8Array && m.cursor) {
          this.store.save(card.id, {
            digest: card.mounted.widget?.digest ?? "",
            stateType: card.mounted.stateType,
            cursor: m.cursor,
            state: m.state,
          });
          this.showFault(card.id, undefined);
        }
        return;
      case "fault":
        this.showFault(card.id, String(m.message));
        return;
      case "action":
        if (m.value instanceof Uint8Array && typeof m.typeUrl === "string") {
          const admin = createClient(WidgetStreamService, this.transport());
          admin.sendAction({ cardId: card.id, typeUrl: m.typeUrl, value: m.value }).catch((err) =>
            this.showFault(card.id, `action refused: ${err instanceof Error ? err.message : String(err)}`),
          );
        }
        return;
      case "storage-get":
        reply({ t: "storage", id: m.id, value: this.store.viewGet(card.id, String(m.key)) });
        return;
      case "storage-set":
        try {
          this.store.viewSet(card.id, String(m.key), m.value === null ? null : String(m.value));
          reply({ t: "storage", id: m.id });
        } catch (err) {
          reply({ t: "storage", id: m.id, error: (err as Error).message });
        }
        return;
      case "local-set": {
        const l = m.local;
        if (l !== null && !(l && typeof l.type === "string" && l.value instanceof Uint8Array)) {
          reply({ t: "storage", id: m.id, error: "malformed local value" });
          return;
        }
        try {
          this.store.localSet(card.id, l);
          reply({ t: "storage", id: m.id });
        } catch (err) {
          reply({ t: "storage", id: m.id, error: (err as Error).message });
        }
        return;
      }
    }
  };
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

if (!customElements.get("widget-board")) customElements.define("widget-board", WidgetBoard);
