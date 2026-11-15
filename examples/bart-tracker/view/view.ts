// The tracker's view: a two-track strip map of the line between the pair's
// stations, with the trains on it, over a split-flap departure board, one
// column each way. The look was settled in spikes/bart-tracker (DESIGN.md).
//
// The server sends absolute times every 20 s or so; between states the view
// moves the trains and counts down from its own clock.

import { defineWidget, type DisplayPrefs } from "@widgetd/sdk";

import {
  StateSchema,
  type Departure,
  type State,
  type Station,
  type Train,
} from "./gen/barttracker/v1/barttracker_pb.ts";
import { setFlaps } from "./flap.ts";
import { clock, inkFor, minutesUntil, shortName, trainLabel, trainPosition, upcoming } from "./model.ts";

const SVG_NS = "http://www.w3.org/2000/svg";
/** The map's height, and its two tracks: outbound (from → to) on top. */
const MAP_H = 145;
const Y_OUT = 26;
const Y_IN = 41; // 15 apart: the tracks just clear, and passing trains (14 across) barely touch
/** Room at each end for a train standing at the end station. */
const PAD = 20;
/** How often trains are moved, and countdowns ticked, from the clock. */
const PLACE_MS = 4000;
/** A revision this big flashes the row. */
const REVISED_MS = 30_000;

// Lucide's train-front (as @widgetd/sdk/icons pins it), drawn inside each train.
const TRAIN_FRONT = [
  "M8 3.1V7a4 4 0 0 0 8 0V3.1",
  "m9 15-1-1",
  "m15 15 1-1",
  "M9 19c-2.8 0-5-2.2-5-5v-4a8 8 0 0 1 16 0v4c0 2.8-2.2 5-5 5Z",
  "m8 19-2 3",
  "m16 19 2 3",
];
// A train: a circle of radius 7 with a point 11 out, the way it's heading.
const DROP_RIGHT = "M4.45 -5.4 A7 7 0 1 0 4.45 5.4 L11 0 Z";
const DROP_LEFT = "M-4.45 -5.4 A7 7 0 1 1 -4.45 5.4 L-11 0 Z";

function svg<K extends keyof SVGElementTagNameMap>(
  tag: K,
  attrs: Record<string, string | number>,
  parent?: Element,
): SVGElementTagNameMap[K] {
  const e = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, String(v));
  parent?.append(e);
  return e;
}

function html<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className: string,
  text?: string,
): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  e.className = className;
  if (text !== undefined) e.textContent = text;
  return e;
}

const afterPaint = (f: () => void) =>
  requestAnimationFrame(() =>
    requestAnimationFrame(() => {
      f();
    }),
  );

interface Marker {
  g: SVGGElement;
  train: Train;
}

interface RowEls {
  el: HTMLElement;
  leave: HTMLElement;
  arrive: HTMLElement;
  min: HTMLElement;
}

/** A shown departure: its row, and the departure as last shown. */
interface Row extends RowEls {
  d: Departure;
}

defineWidget({
  state: StateSchema,

  mount({ root, prefs }) {
    const wrap = html("div", "wrap");
    const head = html("div", "head");
    const pair = html("span", "pair", "BART");
    const asof = html("span", "asof");
    head.append(pair, asof);
    const map = svg("svg", { class: "map", height: MAP_H });
    const stations = svg("g", {}, map);
    const trains = svg("g", {}, map);
    const note = html("div", "note");
    note.hidden = true;
    const cols = html("div", "cols");
    const boards = [0, 1].map(() => {
      const col = html("div", "col");
      const title = html("span", "title");
      const h3 = html("h3", "");
      h3.append(title, html("span", "legend", "leaves in"));
      const list = html("div", "list");
      col.append(h3, list);
      cols.append(col);
      return { title, list, rows: new Map<string, Row>() };
    });
    const tip = html("div", "tip");
    tip.hidden = true;
    wrap.append(head, map, note, cols, tip);
    root.replaceChildren(wrap);

    let line: Station[] = [];
    let drawnFor = "";
    const markers = new Map<string, Marker>();
    let hovered: Marker | undefined;

    const width = () => Math.max(120, wrap.clientWidth);
    const x = (pos: number) => PAD + (line.length > 1 ? pos / (line.length - 1) : 0.5) * (width() - 2 * PAD);

    // The tracks and stations: drawn again only when the line or the width changes.
    const drawMap = () => {
      const key = `${line.map((s) => s.id).join(",")}@${width()}`;
      if (key === drawnFor) return;
      drawnFor = key;
      map.setAttribute("viewBox", `0 0 ${width()} ${MAP_H}`);
      stations.replaceChildren();
      if (!line.length) return;
      // Each track: sleepers (a wide dashed stroke, one dash per tie) under two thin rails.
      for (const y of [Y_OUT, Y_IN]) {
        svg(
          "line",
          {
            x1: x(0),
            x2: x(line.length - 1),
            y1: y,
            y2: y,
            stroke: "#3f3f46",
            "stroke-width": 9,
            "stroke-dasharray": "2 4",
          },
          stations,
        );
        for (const r of [-2.5, 2.5])
          svg(
            "line",
            { x1: x(0), x2: x(line.length - 1), y1: y + r, y2: y + r, stroke: "#8b8b94", "stroke-width": 1.1 },
            stations,
          );
      }
      line.forEach((st, i) => {
        const end = i === 0 || i === line.length - 1;
        const cx = x(i);
        // The pair's stations are platforms across both tracks; the ones between, thin ticks.
        if (end)
          svg(
            "rect",
            { x: cx - 3.5, y: Y_OUT - 9, width: 7, height: Y_IN - Y_OUT + 18, rx: 3.5, fill: "#e5e7eb" },
            stations,
          );
        else
          svg(
            "line",
            { x1: cx, x2: cx, y1: Y_OUT - 9, y2: Y_IN + 9, stroke: "#a1a1aa", "stroke-width": 1.5 },
            stations,
          );
        // Vertical, reading bottom to top, under the lower track's trains;
        // the pair stands out, the stations between are quiet.
        const size = end ? 11 : 8;
        const ly = Y_IN + 18;
        const lx = cx + size * 0.35; // centres the glyphs on the station
        svg(
          "text",
          {
            x: lx,
            y: ly,
            fill: end ? "#e5e7eb" : "#71717a",
            "font-size": size,
            "font-weight": end ? 600 : 400,
            "text-anchor": "end",
            transform: `rotate(-90 ${lx} ${ly})`,
          },
          stations,
        ).textContent = shortName(st.name);
      });
    };

    // A train's tooltip: above its marker (below if there's no room), and
    // never past the card's edges, since nothing outside the frame shows.
    const showTip = (m: Marker | undefined) => {
      hovered = m;
      tip.hidden = !m;
      if (!m) return;
      tip.textContent = trainLabel(m.train, line[m.train.toIndex]?.name ?? "", Date.now());
      const r = m.g.getBoundingClientRect();
      const box = wrap.getBoundingClientRect();
      const inset = 6;
      const gap = 6;
      let top = r.top - box.top - gap - tip.offsetHeight;
      if (top < inset) top = r.bottom - box.top + gap;
      const left = Math.min(
        Math.max(r.left - box.left + r.width / 2 - tip.offsetWidth / 2, inset),
        box.width - inset - tip.offsetWidth,
      );
      tip.style.transform = `translate(${left}px, ${top}px)`;
    };

    const marker = (t: Train): Marker => {
      // A teardrop in the line colour on its track: its point the way it's
      // heading, Lucide's train-front inside.
      const g = svg("g", { class: "train entering" }, trains);
      svg(
        "path",
        {
          d: t.outbound ? DROP_RIGHT : DROP_LEFT,
          fill: `#${t.color}`,
          stroke: "#18181b",
          "stroke-width": 1.5,
          "stroke-linejoin": "round",
          "paint-order": "stroke",
        },
        g,
      );
      const icon = svg(
        "g",
        {
          transform: "translate(-4.8 -4.9) scale(0.4)",
          fill: "none",
          stroke: inkFor(t.color),
          "stroke-width": 2.4,
          "stroke-linecap": "round",
          "stroke-linejoin": "round",
        },
        g,
      );
      for (const d of TRAIN_FRONT) svg("path", { d }, icon);
      const m = { g, train: t };
      g.addEventListener("pointerenter", () => {
        showTip(m);
      });
      g.addEventListener("pointerleave", () => {
        showTip(undefined);
      });
      afterPaint(() => g.classList.remove("entering"));
      return m;
    };

    // Moves the trains and ticks the countdowns, from the clock.
    const place = () => {
      const now = Date.now();
      for (const m of markers.values()) {
        m.g.style.transform = `translate(${x(trainPosition(m.train, now))}px, ${m.train.outbound ? Y_OUT : Y_IN}px)`;
      }
      if (hovered) showTip(hovered);
      asof.textContent = clock(now, prefs).full;
      for (const b of boards)
        for (const r of b.rows.values()) setFlaps(r.min, String(minutesUntil(r.d.leavesUnixMs, now)));
    };

    const update = (s: State) => {
      line = s.line;
      const from = shortName(s.from?.name ?? "");
      const to = shortName(s.to?.name ?? "");
      pair.textContent = `${from} ⇄ ${to}`;
      const direct = line.length > 1;
      map.style.display = direct ? "" : "none";
      note.hidden = direct;
      note.textContent = direct ? "" : `No train runs directly between ${from} and ${to}.`;
      drawMap();

      const seen = new Set<string>();
      for (const t of s.trains) {
        // A train is the same marker while it keeps its trip and direction.
        const key = `${t.trip}:${t.outbound ? "out" : "in"}`;
        seen.add(key);
        const m = markers.get(key) ?? marker(t);
        m.train = t;
        markers.set(key, m);
      }
      // Trains that left the stretch fade out where they are.
      for (const [key, m] of markers) {
        if (seen.has(key)) continue;
        markers.delete(key);
        if (hovered === m) showTip(undefined);
        m.g.classList.add("leaving");
        setTimeout(() => m.g.remove(), 800);
      }

      boards.forEach((b, i) => {
        const [a, z] = i === 0 ? [from, to] : [to, from];
        const strong = html("b", "", a);
        b.title.replaceChildren(strong, ` → ${z}`);
        fillBoard(b, i === 0 ? s.outbound : s.inbound, direct);
      });
      place();
    };

    // One column's rows, kept per train so a change flips in place.
    const fillBoard = (b: (typeof boards)[number], all: Departure[], direct: boolean) => {
      const now = Date.now();
      const deps = upcoming(all, now);
      const keep = new Set(deps.map((d) => d.trip));
      // A train that has left slides out; the rest move up.
      for (const [trip, r] of b.rows) {
        if (keep.has(trip)) continue;
        b.rows.delete(trip);
        r.el.classList.add("leaving");
        setTimeout(() => r.el.remove(), 500);
      }
      deps.forEach((d, i) => {
        let r = b.rows.get(d.trip);
        if (!r) {
          r = { ...row(d, prefs), d };
          r.el.classList.add("entering");
          const el = r.el;
          afterPaint(() => el.classList.remove("entering"));
          b.rows.set(d.trip, r);
        } else if (Math.abs(Number(d.leavesUnixMs - r.d.leavesUnixMs)) >= REVISED_MS) {
          // BART revised it: red if it's now later, green if earlier.
          flash(r.el, d.leavesUnixMs > r.d.leavesUnixMs ? "later" : "earlier");
        }
        r.d = d;
        // The next train is bigger; when it leaves, the one behind grows into its place.
        r.el.classList.toggle("next", i === 0);
        setFlaps(r.leave, clock(d.leavesUnixMs, prefs).short);
        setFlaps(r.arrive, clock(d.arrivesUnixMs, prefs).short);
        setFlaps(r.min, String(minutesUntil(d.leavesUnixMs, now)));
        b.list.append(r.el); // in order; a row sliding out stays where it was
      });
      // Always room for three, so the board's height never jumps.
      b.list.querySelectorAll(".slot, .none").forEach((e) => {
        e.remove();
      });
      if (!deps.length) b.list.append(html("div", "none", direct ? "No trains soon" : "No direct trains"));
      for (let i = Math.max(deps.length, 1); i < 3; i++) {
        const slot = row(undefined, prefs).el;
        slot.classList.add("slot");
        b.list.append(slot);
      }
    };

    const timer = setInterval(place, PLACE_MS);
    const resized = new ResizeObserver(() => {
      drawMap();
      place();
    });
    resized.observe(wrap);
    // Let the first placement land before trains start sliding.
    afterPaint(() => map.classList.add("live"));

    return {
      update,
      destroy() {
        clearInterval(timer);
        resized.disconnect();
      },
    };
  },
});

/** A departure row: line colour, split-flap times, destination, countdown. */
function row(d: Departure | undefined, prefs: DisplayPrefs): RowEls {
  const el = html("div", "dep");
  const bar = html("span", "bar");
  bar.style.background = d ? `#${d.color}` : "transparent";
  const times = html("span", "times");
  const leave = html("span", "roll");
  const arrive = html("span", "roll");
  times.append(leave, html("span", "arrow", "→"), arrive);
  const min = html("span", "min");
  const count = html("span", "roll");
  min.append(count, html("small", "", "min"));
  el.append(bar, times, min, html("span", ""), html("span", "train-info", d ? shortName(d.destination) : "—"));
  // A new row flips into place from blank; a slot just holds the space.
  const at = d ?? { leavesUnixMs: 0n, arrivesUnixMs: 0n };
  setFlaps(leave, clock(at.leavesUnixMs, prefs).short, d !== undefined);
  setFlaps(arrive, clock(at.arrivesUnixMs, prefs).short, d !== undefined);
  setFlaps(count, d ? String(minutesUntil(d.leavesUnixMs, Date.now())) : "0", d !== undefined);
  return { el, leave, arrive, min: count };
}

function flash(el: HTMLElement, kind: "later" | "earlier"): void {
  // Off for a frame first, so a flash already running starts again.
  el.classList.remove("later", "earlier");
  afterPaint(() => {
    el.classList.add(kind);
    setTimeout(() => el.classList.remove(kind), 1800);
  });
}
