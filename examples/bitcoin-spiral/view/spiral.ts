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

export type Cycle = { index: number; start: number; end: number; frac: number };

/** Which halving cycle `t` falls in, and how far through it (0..1). Cycle 1 starts at the first halving. */
export function cycleOf(t: number): Cycle {
  let i = HALVINGS.length - 1;
  while (i > 0 && HALVINGS[i]! > t) i--;
  let start = HALVINGS[i]!;
  let index = i + 1;
  if (t < start) {
    // Before the first halving: count back a cycle at a time.
    const back = Math.ceil((start - t) / CYCLE_MS);
    start -= back * CYCLE_MS;
    index -= back;
  }
  while (start + CYCLE_MS <= t && (index >= HALVINGS.length || HALVINGS[index] === undefined)) {
    start += CYCLE_MS;
    index++;
  }
  const end = HALVINGS[index] ?? start + CYCLE_MS;
  return { index, start, end, frac: (t - start) / (end - start) };
}

export type Scale = { lo: number; hi: number };

/** A log scale from the power of ten at or below `min` to the one at or above `max`. */
export function logScale(min: number, max: number): Scale {
  const lo = Math.floor(Math.log10(Math.max(min, 1e-9)));
  let hi = Math.ceil(Math.log10(Math.max(max, 1e-9)));
  if (hi <= lo) hi = lo + 1;
  return { lo, hi };
}

/** Fraction of the radius a price sits at. */
export const radial = (price: number, s: Scale) => (Math.log10(price) - s.lo) / (s.hi - s.lo);

/** Inner radius fraction kept clear so the first cycle isn't a knot. */
export const HOLE = 0.14;

/** Position of (t, price) in a square of side 2R centred on (R, R). */
export function point(t: number, price: number, s: Scale, R: number): { x: number; y: number; cycle: number } {
  const c = cycleOf(t);
  const a = c.frac * 2 * Math.PI - Math.PI / 2;
  const r = R * (HOLE + (1 - HOLE) * radial(price, s));
  return { x: R + r * Math.cos(a), y: R + r * Math.sin(a), cycle: c.index };
}

/** Ring labels: every power of ten in the scale. */
export function rings(s: Scale): number[] {
  const out: number[] = [];
  for (let e = s.lo; e <= s.hi; e++) out.push(10 ** e);
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
