// The heatmap's arithmetic, apart from the DOM so it can be tested: the
// treemap layout, tile colours, the portfolio's change and number formats.
// Nothing here is money: weights are basis points, moves are percentages.

import type { Holding } from "./gen/portfolio/v1/portfolio_pb.ts";

export type Mode = "day" | "total";

/** Where colour stops getting stronger: ±3 % for a day, ±60 % for total return. */
export const CAP: Record<Mode, number> = { day: 3, total: 60 };

/** A holding's move in the given mode, or undefined if it has none. */
export function moveOf(h: Holding, mode: Mode): number | undefined {
  return mode === "day" ? h.dayPct : h.totalPct;
}

/**
 * The portfolio's move: each holding's, weighted by its share. Holdings
 * without one are left out of the average, not counted as flat. Undefined
 * if none has one.
 */
export function portfolioMove(holdings: readonly Holding[], mode: Mode): number | undefined {
  let w = 0;
  let sum = 0;
  for (const h of holdings) {
    const m = moveOf(h, mode);
    if (m === undefined) continue;
    w += h.weightBp;
    sum += h.weightBp * m;
  }
  return w > 0 ? sum / w : undefined;
}

// ---- colour: red–grey–green, quiet for small moves ----

export type RGB = readonly [number, number, number];

const NEUTRAL: RGB = [58, 64, 74];
const RED: RGB = [229, 49, 68];
const GREEN: RGB = [24, 196, 106];
const DARK_RED: RGB = [120, 38, 48];
const DARK_GREEN: RGB = [28, 98, 64];
/** No move to show: a cooler grey than flat. */
export const UNKNOWN: RGB = [44, 48, 56];
// The edges' hues: brighter and more saturated than any fill.
const VIVID_RED: RGB = [255, 170, 178];
const VIVID_GREEN: RGB = [60, 255, 150];
const VIVID_NEUTRAL: RGB = [118, 128, 142];

const lerp = (a: number, b: number, t: number) => a + (b - a) * t;
export const mix = (a: RGB, b: RGB, t: number): RGB => [
  Math.round(lerp(a[0], b[0], t)),
  Math.round(lerp(a[1], b[1], t)),
  Math.round(lerp(a[2], b[2], t)),
];
export const css = (c: RGB) => `rgb(${c.join(",")})`;

/** A tile's fill: neutral → dark tint → full colour as the move grows to cap. */
export function fill(pct: number | undefined, cap: number): RGB {
  if (pct === undefined) return UNKNOWN;
  if (Math.abs(pct) < cap * 0.03) return NEUTRAL;
  const t = Math.min(1, Math.abs(pct) / cap);
  const [dark, full] = pct < 0 ? [DARK_RED, RED] : [DARK_GREEN, GREEN];
  return t < 0.4 ? mix(NEUTRAL, dark, t / 0.4) : mix(dark, full, (t - 0.4) / 0.6);
}

/** A tile's inset edge: its own fill pushed toward the vivid hue. */
export function edge(pct: number | undefined, cap: number): RGB {
  const vivid = pct === undefined || Math.abs(pct) < cap * 0.03 ? VIVID_NEUTRAL : pct < 0 ? VIVID_RED : VIVID_GREEN;
  return mix(fill(pct, cap), vivid, 0.55);
}

/** "+1.23%", "−0.40%" (a real minus sign), "0.00%"; "—" for no move. */
export function fmt(pct: number | undefined, digits?: number): string {
  if (pct === undefined) return "—";
  const d = digits ?? (Math.abs(pct) >= 100 ? 0 : 2);
  const s = Math.abs(pct).toFixed(d);
  if (Number(s) === 0) return `${(0).toFixed(d)}%`;
  return `${pct > 0 ? "+" : "−"}${s}%`;
}

/** A weight in basis points as a percentage of the portfolio: "14.2%". */
export function weight(bp: number): string {
  return `${(bp / 100).toFixed(bp >= 1000 ? 1 : 2)}%`;
}

// ---- squarified treemap (Bruls, Huizing, van Wijk) ----

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

/**
 * Lays items out in the box (x, y, w, h), each with area in proportion to
 * its value, as near square as it can. Items of no value are left out.
 * Returns rectangles in the items' order, largest value first.
 */
export function squarify<T extends { v: number }>(
  items: readonly T[],
  x: number,
  y: number,
  w: number,
  h: number,
): Array<T & Rect> {
  const out: Array<T & Rect> = [];
  const live = items.filter((i) => i.v > 0);
  const total = live.reduce((s, i) => s + i.v, 0);
  if (!total || w <= 0 || h <= 0) return out;
  const scale = (w * h) / total;
  let rest = live.map((item) => ({ item, a: item.v * scale })).sort((p, q) => q.a - p.a);
  while (rest.length) {
    const side = Math.min(w, h);
    const row = [rest[0]];
    let best = worst(row, side);
    let k = 1;
    while (k < rest.length) {
      const next = worst([...row, rest[k]], side);
      if (next > best) break;
      row.push(rest[k]);
      best = next;
      k++;
    }
    rest = rest.slice(k);
    const sum = row.reduce((s, r) => s + r.a, 0);
    if (w >= h) {
      const cw = sum / h;
      let cy = y;
      for (const r of row) {
        const rh = r.a / cw;
        out.push({ ...r.item, x, y: cy, w: cw, h: rh });
        cy += rh;
      }
      x += cw;
      w -= cw;
    } else {
      const ch = sum / w;
      let cx = x;
      for (const r of row) {
        const rw = r.a / ch;
        out.push({ ...r.item, x: cx, y, w: rw, h: ch });
        cx += rw;
      }
      y += ch;
      h -= ch;
    }
  }
  return out;
}

function worst(row: ReadonlyArray<{ a: number }>, side: number): number {
  const s = row.reduce((t, r) => t + r.a, 0);
  const most = Math.max(...row.map((r) => r.a));
  const least = Math.min(...row.map((r) => r.a));
  return Math.max((side * side * most) / (s * s), (s * s) / (side * side * least));
}

// ---- labels: ticker and %, % only, or colour alone ----

export interface Label {
  sym: boolean;
  pct: boolean;
  /** The ticker's font size, px. */
  symPx: number;
  /** The percentage's font size, px. */
  pctPx: number;
}

/** What fits on a w × h tile for a ticker this long. */
export function label(symbol: string, w: number, h: number): Label {
  const size = Math.min((w - 6) / (0.7 * symbol.length + 0.4), h / 2.3, 30);
  const both = size >= 9 && w > 34 && h > 30;
  const pctOnly = !both && w > 40 && h > 14;
  const pctPx = pctOnly ? Math.min(11, h * 0.6) : Math.max(9, Math.min(size * 0.62, (w - 6) / 3.6));
  return { sym: both, pct: both || pctOnly, symPx: size, pctPx };
}
