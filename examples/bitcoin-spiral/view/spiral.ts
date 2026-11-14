// Spiral geometry, apart from drawing so it can be tested.
//
// One turn of the spiral is one halving cycle: the angle is how far through
// its cycle a day falls (the halving itself at twelve o'clock, clockwise),
// and the radius is the log of the price. Since each cycle has so far traded
// higher than the last, the line winds outward.

const DAY_MS = 86_400_000;

/** Block-reward halvings, UTC. Later ones are estimated a cycle apart. */
const HALVINGS = [Date.UTC(2012, 10, 28), Date.UTC(2016, 6, 9), Date.UTC(2020, 4, 11), Date.UTC(2024, 3, 20)];
const CYCLE_MS = 1461 * DAY_MS;

export interface Cycle {
  index: number;
  start: number;
  end: number;
  frac: number;
}

/** Which halving cycle `t` falls in, and how far through it (0..1). Cycle 1 starts at the first halving. */
export function cycleOf(t: number): Cycle {
  let i = HALVINGS.length - 1;
  while (i > 0 && HALVINGS[i] > t) i--;
  let start = HALVINGS[i];
  let index = i + 1;
  if (t < start) {
    // Before the first halving: count back a cycle at a time.
    const back = Math.ceil((start - t) / CYCLE_MS);
    start -= back * CYCLE_MS;
    index -= back;
  }
  while (start + CYCLE_MS <= t && (index >= HALVINGS.length || (HALVINGS[index] as number | undefined) === undefined)) {
    start += CYCLE_MS;
    index++;
  }
  const end = HALVINGS[index] ?? start + CYCLE_MS;
  return { index, start, end, frac: (t - start) / (end - start) };
}

export interface Scale {
  lo: number;
  hi: number;
}

/**
 * A log scale from `min` itself (so the lowest price sits on the edge of the
 * hole, with no empty band inside it) to the power of ten at or above `max`
 * (so the rim is a round number).
 */
export function logScale(min: number, max: number): Scale {
  const lo = Math.log10(Math.max(min, 1e-9));
  let hi = Math.ceil(Math.log10(Math.max(max, 1e-9)));
  if (hi <= lo) hi = Math.floor(lo) + 1;
  return { lo, hi };
}

/** Fraction of the radius a price sits at. */
export const radial = (price: number, s: Scale) => (Math.log10(price) - s.lo) / (s.hi - s.lo);

/** Inner radius fraction kept clear so the first cycle isn't a knot. */
export const HOLE = 0.14;

/** Position of (t, price) in a square of side 2·outer centred on (outer, outer). */
export function point(
  t: number,
  price: number,
  s: Scale,
  outer: number,
): { x: number; y: number; cycle: number; frac: number } {
  const c = cycleOf(t);
  const a = c.frac * 2 * Math.PI - Math.PI / 2;
  const r = outer * (HOLE + (1 - HOLE) * radial(price, s));
  return { x: outer + r * Math.cos(a), y: outer + r * Math.sin(a), cycle: c.index, frac: c.frac };
}

/** How far through a turn the direction (dx, dy) from the centre points: 0 straight up, clockwise, in [0, 1). */
export function fracAt(dx: number, dy: number): number {
  const f = (Math.atan2(dy, dx) + Math.PI / 2) / (2 * Math.PI);
  return f - Math.floor(f);
}

/**
 * Where a ray at `frac` of a turn crosses the spiral: for each cycle, its point
 * nearest that angle, if one is within `tol` of a turn (so a cycle with no data
 * there, like the one in progress, is left out). Newest cycle, so outermost, first.
 */
export function crossings<P extends { cycle: number; frac: number }>(
  points: readonly P[],
  frac: number,
  tol: number,
): P[] {
  const best = new Map<number, { p: P; d: number }>();
  for (const p of points) {
    const raw = Math.abs(p.frac - frac);
    // Turns wrap: the end of one cycle and the start of the next share the spoke.
    const d = Math.min(raw, 1 - raw);
    const b = best.get(p.cycle);
    if (d <= tol && (!b || d <= b.d)) best.set(p.cycle, { p, d });
  }
  return [...best.entries()].sort((a, b) => b[0] - a[0]).map(([, v]) => v.p);
}

/** Ring labels: every power of ten in the scale. */
export function rings(s: Scale): number[] {
  const out: number[] = [];
  for (let e = Math.ceil(s.lo); e <= s.hi; e++) out.push(10 ** e);
  return out;
}

export const dayToMs = (day: number) => day * DAY_MS;

const SYMBOLS: Record<number, string> = { 1: "$", 2: "€", 3: "£" };

/** "$97,123" or, compactly for ring labels, "$100K". */
export function formatPrice(price: number, currency: number, compact = false): string {
  const sym = SYMBOLS[currency] ?? "";
  if (compact) {
    if (price >= 1e6) return `${sym}${price / 1e6}M`;
    if (price >= 1e3) return `${sym}${price / 1e3}K`;
    return `${sym}${price}`;
  }
  return sym + price.toLocaleString("en-US", { maximumFractionDigits: price < 100 ? 2 : 0 });
}

/** Percent change from `from` to `to`, e.g. "+1.24%". */
export function formatChange(from: number, to: number): string {
  const pct = ((to - from) / from) * 100;
  return `${pct >= 0 ? "+" : "−"}${Math.abs(pct).toFixed(2)}%`;
}

/** "Mar 12, 2021" for a UTC day, or with the local time for a live quote. */
export function formatWhen(t: number, withTime: boolean): string {
  return withTime
    ? new Date(t).toLocaleString("en-US", { dateStyle: "medium", timeStyle: "short" })
    : new Date(t).toLocaleDateString("en-US", { dateStyle: "medium", timeZone: "UTC" });
}
