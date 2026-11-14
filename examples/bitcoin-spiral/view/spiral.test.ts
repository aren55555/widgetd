import { describe, expect, test } from "bun:test";
import {
  crossings,
  cycleOf,
  formatChange,
  formatPrice,
  formatWhen,
  fracAt,
  logScale,
  point,
  radial,
  rings,
} from "./spiral.ts";

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
  test("starts at the lowest price and ends at a power of ten", () => {
    const s = logScale(180, 120_000);
    expect(s.lo).toBeCloseTo(Math.log10(180));
    expect(s.hi).toBe(6);
  });

  test("rings are the powers of ten inside the scale", () => {
    expect(rings({ lo: 2, hi: 4 })).toEqual([100, 1000, 10000]);
    expect(rings(logScale(0.06, 120_000))).toEqual([0.1, 1, 10, 100, 1000, 10000, 100000, 1000000]);
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

describe("fracAt", () => {
  test("straight up is 0, and it runs clockwise", () => {
    expect(fracAt(0, -1)).toBe(0);
    expect(fracAt(1, 0)).toBeCloseTo(0.25);
    expect(fracAt(0, 1)).toBeCloseTo(0.5);
    expect(fracAt(-1, 0)).toBeCloseTo(0.75);
  });

  test("agrees with point", () => {
    const p = point(utc(2018, 6, 1), 1000, { lo: 2, hi: 4 }, 100);
    expect(fracAt(p.x - 100, p.y - 100)).toBeCloseTo(p.frac);
  });
});

describe("crossings", () => {
  const pts = [
    { cycle: 1, frac: 0.3, id: "a" },
    { cycle: 1, frac: 0.31, id: "b" },
    { cycle: 2, frac: 0.305, id: "c" },
    { cycle: 3, frac: 0.1, id: "d" },
  ];
  const ids = (ps: Array<{ id: string }>) => ps.map((p) => p.id);

  test("takes each cycle's nearest point, newest first", () => {
    expect(ids(crossings(pts, 0.309, 0.01))).toEqual(["c", "b"]);
  });

  test("leaves out a cycle with nothing near the angle", () => {
    expect(ids(crossings(pts, 0.1, 0.01))).toEqual(["d"]);
    expect(crossings(pts, 0.7, 0.01)).toEqual([]);
  });

  test("wraps around the halving spoke", () => {
    const wrap = [
      { cycle: 1, frac: 0.999, id: "end" },
      { cycle: 2, frac: 0.001, id: "start" },
    ];
    expect(ids(crossings(wrap, 0, 0.01))).toEqual(["start", "end"]);
  });
});

describe("formatWhen", () => {
  test("a UTC day has no time and no timezone drift", () => {
    expect(formatWhen(utc(2021, 3, 12), false)).toBe("Mar 12, 2021");
  });

  test("a live quote includes the time", () => {
    expect(formatWhen(utc(2021, 3, 12) + 12 * 3600_000, true)).toMatch(/2021.*\d:\d\d/);
  });
});
