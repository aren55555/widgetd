import { defineWidget } from "@widgetd/sdk";
import { create } from "@bufbuild/protobuf";

import { DeltaSchema, StateSchema, type State } from "./gen/bitcoinspiral/v1/bitcoinspiral_pb.ts";
import {
  cycleOf,
  dayToMs,
  formatChange,
  formatPrice,
  formatWhen,
  fracAt,
  crossings,
  logScale,
  point,
  rings,
  HOLE,
  type Scale,
} from "./spiral.ts";

/** A point as drawn, in canvas CSS pixels, so the pointer can find it. */
interface Plotted {
  x: number;
  y: number;
  t: number;
  price: number;
  live: boolean;
  cycle: number;
  /** How far through its cycle, so the angle it sits at. */
  frac: number;
}

/** Where the spiral was drawn, in canvas CSS pixels. */
interface Layout {
  plotted: Plotted[];
  cx: number;
  cy: number;
  outer: number;
}

/** How far off the hovered angle (in turns, ~2 days) a cycle's nearest close may be and still count as a crossing. */
const RAY_TOL = 2 / 1461;

/** How far behind the hover ray its scan trail reaches, in turns. */
const SCAN_TRAIL = 0.2;

const SPOKES = 12;
const RIM_TICKS = 120;

defineWidget({
  state: StateSchema,
  delta: DeltaSchema,

  apply(state, delta) {
    switch (delta.change.case) {
      case "quote":
        state.latest = delta.change.value;
        break;
      case "close": {
        const c = delta.change.value;
        const last = state.daily.at(-1);
        if (last?.day === c.day) state.daily[state.daily.length - 1] = c;
        else if (!last || last.day < c.day) state.daily.push(c);
        break;
      }
      case undefined:
        // An empty delta: nothing changed.
        break;
    }
    return state;
  },

  mount({ root }) {
    root.innerHTML = `
      <div class="wrap">
        <canvas></canvas>
        <canvas class="ray"></canvas>
        <div class="readout">
          <div class="price">—</div>
          <div class="change"></div>
          <div class="cycle"></div>
        </div>
        <div class="tip" hidden></div>
      </div>`;
    const [canvas, ray] = root.querySelectorAll("canvas");
    const price = root.querySelector<HTMLElement>(".price")!;
    const change = root.querySelector<HTMLElement>(".change")!;
    const cycle = root.querySelector<HTMLElement>(".cycle")!;
    const tip = root.querySelector<HTMLElement>(".tip")!;
    let current = create(StateSchema);
    let layout: Layout = { plotted: [], cx: 0, cy: 0, outer: 0 };
    let pointer: { x: number; y: number } | null = null;

    // Hovering anywhere on the spiral draws a radar scan: a beam from the
    // centre at that angle with a fading trail behind it. It marks where the
    // beam crosses each cycle and lists those days.
    const hover = () => {
      const g = ray.getContext("2d");
      if (!g) return;
      const dpr = window.devicePixelRatio || 1;
      g.setTransform(dpr, 0, 0, dpr, 0, 0);
      g.clearRect(0, 0, ray.width, ray.height);
      tip.hidden = true;
      const { plotted, cx, cy, outer } = layout;
      if (!pointer || !plotted.length) return;
      const dx = pointer.x - cx;
      const dy = pointer.y - cy;
      if (Math.hypot(dx, dy) > outer + 8) return;

      const pal = palette();
      const frac = fracAt(dx, dy);
      const a = frac * 2 * Math.PI - Math.PI / 2;
      const trail = SCAN_TRAIL * 2 * Math.PI;
      const grad = g.createConicGradient(a - trail, cx, cy);
      grad.addColorStop(0, rgba(pal.ink, 0));
      grad.addColorStop(SCAN_TRAIL, rgba(pal.ink, 0.22));
      grad.addColorStop(Math.min(1, SCAN_TRAIL + 0.001), rgba(pal.ink, 0));
      grad.addColorStop(1, rgba(pal.ink, 0));
      g.fillStyle = grad;
      g.beginPath();
      g.moveTo(cx, cy);
      g.arc(cx, cy, outer, a - trail, a);
      g.closePath();
      g.fill();
      g.strokeStyle = rgba(pal.ink, 0.8);
      g.shadowColor = pal.ink;
      g.shadowBlur = 6;
      g.lineWidth = 1.5;
      g.beginPath();
      g.moveTo(cx, cy);
      g.lineTo(cx + outer * Math.cos(a), cy + outer * Math.sin(a));
      g.stroke();
      g.shadowBlur = 0;

      const hits = crossings(plotted, frac, RAY_TOL);
      if (!hits.length) return;
      // The crossing nearest the pointer is the one being pointed at.
      const near = hits.reduce((b, p) =>
        Math.hypot(p.x - pointer!.x, p.y - pointer!.y) < Math.hypot(b.x - pointer!.x, b.y - pointer!.y) ? p : b,
      );
      // Each crossing is a bogie: a red contact, the one pointed at locked with a ring.
      for (const p of hits) {
        g.fillStyle = pal.bogie;
        g.strokeStyle = pal.scope;
        g.lineWidth = 2;
        g.shadowColor = pal.bogie;
        g.shadowBlur = 10;
        g.beginPath();
        g.arc(p.x, p.y, p === near ? 4.5 : 3.5, 0, 2 * Math.PI);
        g.fill();
        g.shadowBlur = 0;
        g.stroke();
        if (p === near) {
          g.strokeStyle = rgba(pal.bogie, 0.7);
          g.lineWidth = 1;
          g.beginPath();
          g.arc(p.x, p.y, 9, 0, 2 * Math.PI);
          g.stroke();
        }
      }

      tip.replaceChildren(
        ...hits.map((p) => {
          const row = document.createElement("div");
          row.className = "row";
          if (p === near) row.dataset.near = "true";
          const when = document.createElement("span");
          when.className = "when";
          when.textContent = formatWhen(p.t, p.live);
          const val = document.createElement("span");
          val.className = "val";
          val.textContent = formatPrice(p.price, current.currency);
          row.append(when, val);
          return row;
        }),
      );
      tip.hidden = false;
      // Off to one side of the ray, so it never covers the line or a crossing:
      // a box in the quadrant the ray's normal points into can't meet the ray.
      // If that side would leave the card, use the other.
      const { width, height } = canvas.getBoundingClientRect();
      const { x, y } = pointer;
      const w = tip.offsetWidth;
      const h = tip.offsetHeight;
      const place = (nx: number, ny: number) => ({
        l: nx > 0 ? x + 12 : x - 12 - w,
        t: ny > 0 ? y + 12 : y - 12 - h,
      });
      let at = place(-dy, dx);
      if (at.l < 0 || at.t < 0 || at.l + w > width || at.t + h > height) at = place(dy, -dx);
      const l = Math.min(Math.max(0, at.l), width - w);
      const t = Math.min(Math.max(0, at.t), height - h);
      tip.style.transform = `translate(${l}px, ${t}px)`;
    };

    const redraw = () => {
      layout = draw(canvas, current);
      ray.width = canvas.width;
      ray.height = canvas.height;
      readout(current, price, change, cycle);
      hover();
    };
    const move = (e: PointerEvent) => {
      const r = canvas.getBoundingClientRect();
      pointer = { x: e.clientX - r.left, y: e.clientY - r.top };
      hover();
    };
    const leave = () => {
      pointer = null;
      hover();
    };
    canvas.addEventListener("pointermove", move);
    canvas.addEventListener("pointerdown", move);
    canvas.addEventListener("pointerleave", leave);
    const ro = new ResizeObserver(redraw);
    ro.observe(canvas);

    return {
      update(state) {
        current = state;
        redraw();
      },
      destroy: () => {
        ro.disconnect();
        canvas.removeEventListener("pointermove", move);
        canvas.removeEventListener("pointerdown", move);
        canvas.removeEventListener("pointerleave", leave);
      },
    };
  },
});

/** The radar's colours, from view.css (hex, so alphas can be derived). */
interface Palette {
  scope: string;
  ink: string;
  blip: string;
  bogie: string;
}

function palette(): Palette {
  return {
    scope: css("--radar-scope", "#03110a"),
    ink: css("--radar-ink", "#4dff9a"),
    blip: css("--radar-blip", "#d9ffe9"),
    bogie: css("--radar-bogie", "#ff3b3b"),
  };
}

function css(name: string, fallback: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
}

/** "#4dff9a" at alpha `a`, as rgba(). */
function rgba(hex: string, a: number): string {
  const n = parseInt(hex.slice(1, 7), 16);
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${a})`;
}

function readout(s: State, price: HTMLElement, change: HTMLElement, cycle: HTMLElement) {
  const latest = s.latest?.price ?? s.daily.at(-1)?.close;
  if (latest === undefined) {
    price.textContent = "loading…";
    return;
  }
  price.textContent = formatPrice(latest, s.currency);
  const prev = s.daily.at(-1)?.close;
  if (s.latest && prev) {
    change.textContent = `${formatChange(prev, s.latest.price)} today`;
    change.dataset.dir = s.latest.price >= prev ? "up" : "down";
  }
  const c = cycleOf(Date.now());
  cycle.textContent = `cycle ${c.index} · ${Math.round(c.frac * 100)}% to next halving`;
}

/** Draws the scope and the spiral and returns where: every point on it, and its centre and radius, for hovering. */
function draw(canvas: HTMLCanvasElement, s: State): Layout {
  const dpr = window.devicePixelRatio || 1;
  const { width, height } = canvas.getBoundingClientRect();
  canvas.width = Math.round(width * dpr);
  canvas.height = Math.round(height * dpr);
  const g = canvas.getContext("2d");
  if (!g || !s.daily.length) return { plotted: [], cx: 0, cy: 0, outer: 0 };
  g.scale(dpr, dpr);

  const size = Math.min(width, height);
  const outer = size / 2 - 6;
  const ox = (width - size) / 2 + 6;
  const oy = (height - size) / 2 + 6;
  g.translate(ox, oy);
  const plotted: Plotted[] = [];
  const pal = palette();

  const prices = s.daily.map((d) => d.close);
  if (s.latest) prices.push(s.latest.price);
  const scale = logScale(Math.min(...prices), Math.max(...prices));

  drawScope(g, scale, outer, pal);

  // One trace per cycle, glowing, older cycles fainter, so the turns read apart.
  const byCycle = new Map<number, Array<{ x: number; y: number }>>();
  for (const d of s.daily) {
    const t = dayToMs(d.day);
    const p = point(t, d.close, scale, outer);
    plotted.push({ x: p.x + ox, y: p.y + oy, t, price: d.close, live: false, cycle: p.cycle, frac: p.frac });
    (byCycle.get(p.cycle) ?? byCycle.set(p.cycle, []).get(p.cycle)!).push(p);
  }
  const newest = Math.max(...byCycle.keys());
  g.strokeStyle = pal.ink;
  g.shadowColor = pal.ink;
  g.lineJoin = "round";
  for (const [c, pts] of byCycle) {
    g.globalAlpha = Math.max(0.25, 1 - (newest - c) * 0.25);
    g.lineWidth = c === newest ? 1.75 : 1.25;
    g.shadowBlur = c === newest ? 8 : 4;
    g.beginPath();
    pts.forEach((p, i) => (i ? g.lineTo(p.x, p.y) : g.moveTo(p.x, p.y)));
    g.stroke();
  }
  g.globalAlpha = 1;

  if (s.latest) {
    const t = Number(s.latest.unixMs);
    const p = point(t, s.latest.price, scale, outer);
    plotted.push({ x: p.x + ox, y: p.y + oy, t, price: s.latest.price, live: true, cycle: p.cycle, frac: p.frac });
    // A blip: a soft halo round a bright core.
    g.shadowBlur = 0;
    g.fillStyle = rgba(pal.ink, 0.18);
    g.beginPath();
    g.arc(p.x, p.y, 10, 0, 2 * Math.PI);
    g.fill();
    g.fillStyle = pal.blip;
    g.shadowColor = pal.ink;
    g.shadowBlur = 12;
    g.beginPath();
    g.arc(p.x, p.y, 3.5, 0, 2 * Math.PI);
    g.fill();
  }
  g.shadowBlur = 0;
  drawLabels(g, scale, outer, s.currency, pal);
  return { plotted, cx: ox + outer, cy: oy + outer, outer };
}

const ringRadius = (v: number, scale: Scale, outer: number) =>
  outer * (HOLE + (1 - HOLE) * ((Math.log10(v) - scale.lo) / (scale.hi - scale.lo)));

/** The scope: a dark disc, faint range rings (one per power of ten), spokes, and a ticked rim. */
function drawScope(g: CanvasRenderingContext2D, scale: Scale, outer: number, pal: Palette) {
  g.fillStyle = pal.scope;
  g.beginPath();
  g.arc(outer, outer, outer, 0, 2 * Math.PI);
  g.fill();

  g.lineWidth = 1;
  g.strokeStyle = rgba(pal.ink, 0.12);
  for (const v of rings(scale)) {
    g.beginPath();
    g.arc(outer, outer, ringRadius(v, scale, outer), 0, 2 * Math.PI);
    g.stroke();
  }

  // Twelve spokes, the halving one (straight up) a little brighter.
  for (let i = 0; i < SPOKES; i++) {
    const a = (i / SPOKES) * 2 * Math.PI - Math.PI / 2;
    g.strokeStyle = rgba(pal.ink, i === 0 ? 0.2 : 0.07);
    g.beginPath();
    g.moveTo(outer + outer * HOLE * Math.cos(a), outer + outer * HOLE * Math.sin(a));
    g.lineTo(outer + outer * Math.cos(a), outer + outer * Math.sin(a));
    g.stroke();
  }

  // The rim, ticked like a bearing scale: long at each spoke, short between.
  g.strokeStyle = rgba(pal.ink, 0.4);
  g.beginPath();
  g.arc(outer, outer, outer, 0, 2 * Math.PI);
  g.stroke();
  for (let i = 0; i < RIM_TICKS; i++) {
    const a = (i / RIM_TICKS) * 2 * Math.PI;
    const len = i % (RIM_TICKS / SPOKES) === 0 ? 8 : 3;
    g.strokeStyle = rgba(pal.ink, len > 3 ? 0.45 : 0.25);
    g.beginPath();
    g.moveTo(outer + outer * Math.cos(a), outer + outer * Math.sin(a));
    g.lineTo(outer + (outer - len) * Math.cos(a), outer + (outer - len) * Math.sin(a));
    g.stroke();
  }
}

/** Ring labels go on last, on a backing, so the trace never runs through them. */
function drawLabels(g: CanvasRenderingContext2D, scale: Scale, outer: number, currency: number, pal: Palette) {
  g.font = `10px ${css("--wd-font-mono", "ui-monospace, monospace")}`;
  g.textAlign = "center";
  g.textBaseline = "middle";
  for (const v of rings(scale)) {
    const r = ringRadius(v, scale, outer);
    const text = formatPrice(v, currency, true);
    const w = g.measureText(text).width + 6;
    g.fillStyle = pal.scope;
    g.fillRect(outer - w / 2, outer - r - 6, w, 12);
    g.fillStyle = rgba(pal.ink, 0.55);
    g.fillText(text, outer, outer - r);
  }
}
