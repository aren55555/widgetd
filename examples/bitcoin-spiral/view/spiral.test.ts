import { describe, expect, test } from "bun:test";
import { cycleOf, formatChange, formatPrice, logScale, point, radial, rings } from "./spiral.ts";

const utc = (y: number, m: number, d: number) => Date.UTC(y, m - 1, d);

describe("cycleOf", () => {
  test("a halving starts its cycle", () => {
    const c = cycleOf(utc(2024, 4, 20));
    expect(c.index).toBe(4);
    expect(c.frac).toBe(0);
  });

  test("measures progress against the actual next halving", () => {
    const c = cycleOf(utc(2018, 6, 1));
    expect(c.index).toBe(2);
    expect(c.end).toBe(utc(2020, 5, 11));
    expect(c.frac).toBeGreaterThan(0.45);
    expect(c.frac).toBeLessThan(0.5);
  });

  test("estimates cycles past the last known halving", () => {
    const c = cycleOf(utc(2029, 1, 1));
    expect(c.index).toBe(5);
    expect(c.frac).toBeGreaterThanOrEqual(0);
    expect(c.frac).toBeLessThan(1);
  });

  test("counts back before the first halving", () => {
    expect(cycleOf(utc(2011, 1, 1)).index).toBe(0);
  });
});

describe("scale", () => {
  test("snaps to powers of ten", () => {
    expect(logScale(180, 120_000)).toEqual({ lo: 2, hi: 6 });
    expect(rings({ lo: 2, hi: 4 })).toEqual([100, 1000, 10000]);
  });

  test("never collapses", () => {
    expect(logScale(100, 100)).toEqual({ lo: 2, hi: 3 });
  });

  test("radial is 0 at the bottom and 1 at the top", () => {
    expect(radial(100, { lo: 2, hi: 4 })).toBe(0);
    expect(radial(10_000, { lo: 2, hi: 4 })).toBe(1);
  });

  test("a halving sits straight up from the centre", () => {
    const p = point(utc(2020, 5, 11), 10_000, { lo: 2, hi: 4 }, 100);
    expect(p.x).toBeCloseTo(100);
    expect(p.y).toBeCloseTo(0);
  });
});

describe("format", () => {
  test("prices", () => {
    expect(formatPrice(97123.4, 1)).toBe("$97,123");
    expect(formatPrice(100_000, 2, true)).toBe("€100K");
    expect(formatPrice(1_000_000, 3, true)).toBe("£1M");
  });

  test("changes", () => {
    expect(formatChange(100, 101.5)).toBe("+1.50%");
    expect(formatChange(100, 98)).toBe("−2.00%");
  });
});
