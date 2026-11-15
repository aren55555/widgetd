// The portfolio heatmap card. One treemap of every holding: a tile's area
// is its weight in the portfolio, its colour its move today (or its total
// return: the toggle in the header, kept per screen). Tapping a tile grows
// it to fill the map and flips it over to its details; tapping again, or
// leaving it for 30 s, puts it back. No dollar amount reaches this view:
// the state has only weights and percentages. The arithmetic is in model.ts.

import { defineWidget } from "@widgetd/sdk";

import {
  LocalSchema,
  Mode as LocalMode,
  StateSchema,
  type Holding,
  type State,
} from "./gen/portfolio/v1/portfolio_pb.ts";
import { CAP, css, edge, fill, fmt, label, moveOf, portfolioMove, squarify, weight, type Mode } from "./model.ts";

/** How long an open tile stays open untouched. */
const IDLE_MS = 30_000;
/** How long the grow-and-flip takes; matches view.css. */
const FLIP_MS = 700;

const WORDS: Record<Mode, { short: string; long: string }> = {
  day: { short: "Today", long: "today" },
  total: { short: "Total", long: "all time" },
};

function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls?: string, text?: string): HTMLElementTagNameMap[K] {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
}

const upDown = (p: number | undefined) => (p === undefined ? "" : p >= 0 ? "up" : "down");

interface Tile {
  node: HTMLDivElement;
  sym: HTMLDivElement;
  pct: HTMLDivElement;
  /** The move it last showed, to flash it when that changes. */
  shown?: number;
}

interface Open {
  symbol: string;
  flip: HTMLDivElement;
  front: HTMLDivElement;
  back: HTMLDivElement;
  body: HTMLDivElement;
  tile: Tile;
  closing: boolean;
  covered: number;
}

defineWidget({
  state: StateSchema,
  local: LocalSchema,
  mount({ root, local }) {
    root.style.setProperty("--hair", `${1 / (window.devicePixelRatio || 1)}px`);
    const card = el("div", "pf");
    const head = el("div", "head");
    const title = el("span", "title", "Portfolio");
    const move = el("span", "move");
    const moveText = el("span");
    const moveWord = el("small");
    move.append(moveText, moveWord);
    const toggle = el("span", "toggle");
    const buttons = (["day", "total"] as const).map((m) => {
      const b = el("button", undefined, WORDS[m].short);
      b.type = "button";
      b.addEventListener("click", (e) => {
        e.stopPropagation();
        void local.set({ ...local.value, mode: m === "day" ? LocalMode.DAY : LocalMode.TOTAL }).catch(() => {
          // Couldn't save it: the screen still shows the new mode this time.
        });
        render();
      });
      toggle.append(b);
      return [m, b] as const;
    });
    head.append(title, move, toggle);
    const map = el("div", "map");
    const empty = el("div", "empty");
    card.append(head, map, empty);
    root.replaceChildren(card);

    let st: State | undefined;
    const tiles = new Map<string, Tile>();
    let open: Open | undefined;
    let idle = 0;

    /** The mode to draw: the screen's choice, unless there's nothing to show for today. */
    const mode = (): Mode => {
      const chosen: Mode = local.value.mode === LocalMode.TOTAL ? "total" : "day";
      if (chosen === "day" && st && !st.holdings.some((h) => h.dayPct !== undefined)) return "total";
      return chosen;
    };

    function render() {
      const holdings = st?.holdings ?? [];
      const m = mode();
      const cap = CAP[m];
      const noToday = !!st && !st.holdings.some((h) => h.dayPct !== undefined);

      // ---- header ----
      const pm = portfolioMove(holdings, m);
      moveText.textContent = fmt(pm);
      move.className = `move ${upDown(pm)}`;
      moveWord.textContent = WORDS[m].long;
      move.hidden = pm === undefined;
      toggle.hidden = noToday || card.clientWidth < 360;
      for (const [k, b] of buttons) b.classList.toggle("on", k === m);
      title.hidden = card.clientWidth < 260;

      // ---- empty ----
      empty.hidden = holdings.length > 0;
      if (!st || (holdings.length === 0 && st.asOfUnixMs === 0n)) empty.textContent = "Reading your holdings…";
      else empty.textContent = "No holdings to show.";

      // ---- tiles ----
      const W = map.clientWidth;
      const H = map.clientHeight;
      const rects = squarify(
        holdings.map((h) => ({ h, v: h.weightBp })),
        0,
        0,
        W,
        H,
      );
      const seen = new Set<string>();
      for (const r of rects) {
        const h = r.h;
        seen.add(h.symbol);
        let t = tiles.get(h.symbol);
        if (!t) {
          const node = el("div", "tile");
          const sym = el("div", "sym");
          const pct = el("div", "pct");
          node.append(sym, pct);
          node.addEventListener("click", () => openTile(h.symbol));
          map.append(node);
          t = { node, sym, pct };
          tiles.set(h.symbol, t);
        }
        const v = moveOf(h, m);
        Object.assign(t.node.style, {
          left: `${r.x}px`,
          top: `${r.y}px`,
          width: `${r.w}px`,
          height: `${r.h}px`,
          background: css(fill(v, cap)),
          boxShadow: `inset 0 0 0 var(--hair) ${css(edge(v, cap))}`,
        });
        const lab = label(h.symbol, r.w, r.h);
        t.sym.textContent = lab.sym ? h.symbol : "";
        t.sym.style.fontSize = `${lab.symPx}px`;
        t.pct.textContent = lab.pct ? fmt(v) : "";
        t.pct.style.fontSize = `${lab.pctPx}px`;
        t.node.title = `${h.symbol}${h.name ? ` · ${h.name}` : ""}: ${fmt(v)} ${WORDS[m].long}`;
        if (t.shown !== undefined && v !== undefined && t.shown !== v) {
          t.node.classList.remove("flash");
          void t.node.getBoundingClientRect(); // a reflow, to restart the animation
          t.node.classList.add("flash");
        }
        t.shown = v;
      }
      for (const [s, t] of tiles) {
        if (seen.has(s)) continue;
        if (open?.symbol === s) closeTile(true);
        t.node.remove();
        tiles.delete(s);
      }
      if (open && !open.closing) {
        const h = holdings.find((x) => x.symbol === open?.symbol);
        if (h) renderBack(h);
      }
    }

    // ---- a tapped tile grows to fill the map and flips to its back ----

    const rectIn = (node: HTMLElement) => {
      const a = node.getBoundingClientRect();
      const b = card.getBoundingClientRect();
      return { left: a.left - b.left, top: a.top - b.top, width: a.width, height: a.height };
    };
    const place = (node: HTMLElement, r: { left: number; top: number; width: number; height: number }) => {
      Object.assign(node.style, {
        left: `${r.left}px`,
        top: `${r.top}px`,
        width: `${r.width}px`,
        height: `${r.height}px`,
      });
    };
    const arm = () => {
      clearTimeout(idle);
      idle = window.setTimeout(() => {
        closeTile();
      }, IDLE_MS);
    };

    function openTile(symbol: string) {
      const tile = tiles.get(symbol);
      const h = st?.holdings.find((x) => x.symbol === symbol);
      if (open || !tile || !h) return;
      const flip = el("div", "flip");
      const inner = el("div", "flip-inner");
      const front = el("div", "face front");
      const back = el("div", "face back");
      const body = el("div", "back-body");
      back.append(body);
      inner.append(front, back);
      flip.append(inner);
      front.append(tile.sym.cloneNode(true), tile.pct.cloneNode(true));
      front.style.background = tile.node.style.background;
      front.style.boxShadow = tile.node.style.boxShadow;
      place(flip, rectIn(tile.node));
      card.append(flip);
      tile.node.style.visibility = "hidden";
      open = { symbol, flip, front, back, body, tile, closing: false, covered: 0 };
      renderBack(h);
      void flip.getBoundingClientRect(); // a reflow, so it starts from the tile's box
      flip.classList.add("moving", "flipped");
      place(flip, { ...rectIn(map), width: map.clientWidth, height: map.clientHeight });
      // Once it's over, hide the map beneath, so no edge shows through.
      open.covered = window.setTimeout(() => map.classList.add("covered"), FLIP_MS);
      flip.addEventListener("click", () => {
        closeTile();
      });
      arm();
    }

    function closeTile(now = false) {
      if (!open || open.closing) return;
      const o = open;
      o.closing = true;
      clearTimeout(idle);
      clearTimeout(o.covered);
      map.classList.remove("covered");
      const done = () => {
        o.tile.node.style.visibility = "";
        o.flip.remove();
        if (open === o) open = undefined;
      };
      if (now) {
        done();
        return;
      }
      o.flip.classList.remove("flipped");
      place(o.flip, rectIn(o.tile.node));
      window.setTimeout(done, FLIP_MS);
    }

    function renderBack(h: Holding) {
      if (!open) return;
      const m = mode();
      const W = map.clientWidth;
      const H = map.clientHeight;
      const tiny = W < 300;
      const v = moveOf(h, m);
      const [r, g, b] = fill(h.dayPct ?? h.totalPct, h.dayPct !== undefined ? CAP.day : CAP.total);
      open.back.style.background = `linear-gradient(180deg, rgb(${r} ${g} ${b} / 0.45) 0%, var(--card) 55%), var(--card)`;
      open.back.style.boxShadow = open.tile.node.style.boxShadow;
      open.front.style.boxShadow = open.tile.node.style.boxShadow;

      const body = open.body;
      body.style.width = `${W}px`;
      body.style.height = `${H}px`;
      body.classList.toggle("tiny", tiny);

      const top = el("div", "back-head");
      top.append(el("span", "sym", h.symbol), el("span", "name", h.name), el("span", "x", "✕"));
      const big = el("div", "big");
      const bigN = el("b", upDown(v), fmt(v, v !== undefined && Math.abs(v) >= 100 ? 1 : 2));
      big.append(
        bigN,
        el("small", undefined, m === "day" ? (h.crypto ? "past 24 hours" : "today") : "since you bought it"),
      );

      const stats = el("div", "stats");
      const stat = (name: string, value: string, cls = "") => {
        const d = el("div");
        d.append(el("span", undefined, name), el("b", cls, value));
        stats.append(d);
      };
      stat("Weight", weight(h.weightBp));
      stat(h.crypto ? "24 h" : "Today", fmt(h.dayPct), upDown(h.dayPct));
      stat("Total return", fmt(h.totalPct, 1), upDown(h.totalPct));
      body.replaceChildren(top, big, stats);
    }

    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") closeTile();
    };
    window.addEventListener("keydown", onKey);
    const resize = new ResizeObserver(() => {
      if (open) closeTile(true);
      render();
    });
    resize.observe(map);

    return {
      update(state) {
        st = state;
        render();
      },
      destroy() {
        clearTimeout(idle);
        resize.disconnect();
        window.removeEventListener("keydown", onKey);
      },
    };
  },
});
