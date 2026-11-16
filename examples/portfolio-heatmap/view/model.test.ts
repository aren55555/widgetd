import { create } from "@bufbuild/protobuf";
import { describe, expect, test } from "bun:test";

import { HoldingSchema, type Holding } from "./gen/portfolio/v1/portfolio_pb.ts";
import {
  CAP,
  css,
  edge,
  fill,
  fmt,
  label,
  portfolioMove,
  spanLabel,
  spark,
  squarify,
  UNKNOWN,
  weight,
} from "./model.ts";

const h = (symbol: string, weightBp: number, dayPct?: number, totalPct?: number): Holding =>
  create(HoldingSchema, { symbol, weightBp, dayPct, totalPct });

describe("portfolioMove", () => {
  test("weights each holding's move by its share", () => {
    expect(portfolioMove([h("A", 7500, 2), h("B", 2500, -2)], "day")).toBeCloseTo(1);
  });
  test("leaves out holdings with no move rather than counting them flat", () => {
    expect(portfolioMove([h("A", 5000, 4), h("B", 5000)], "day")).toBeCloseTo(4);
  });
  test("is undefined when nothing has a move", () => {
    expect(portfolioMove([h("A", 10000)], "total")).toBeUndefined();
    expect(portfolioMove([], "day")).toBeUndefined();
  });
});

describe("colour", () => {
  test("small moves are neutral, and unknown is its own grey", () => {
    expect(fill(0.05, CAP.day)).toEqual(fill(-0.05, CAP.day));
    expect(fill(undefined, CAP.day)).toEqual(UNKNOWN);
    expect(fill(undefined, CAP.day)).not.toEqual(fill(0, CAP.day));
  });
  test("saturates at the cap", () => {
    expect(fill(3, CAP.day)).toEqual(fill(30, CAP.day));
    expect(fill(-60, CAP.total)).toEqual(fill(-600, CAP.total));
  });
  test("up is green, down is red", () => {
    const [ur, ug] = fill(2, CAP.day);
    const [dr, dg] = fill(-2, CAP.day);
    expect(ug).toBeGreaterThan(ur);
    expect(dr).toBeGreaterThan(dg);
  });
  test("the edge is brighter than the fill", () => {
    const sum = (c: readonly number[]) => c.reduce((a, b) => a + b, 0);
    expect(sum(edge(1, CAP.day))).toBeGreaterThan(sum(fill(1, CAP.day)));
    expect(css([1, 2, 3])).toBe("rgb(1,2,3)");
  });
});

describe("formats", () => {
  test("percentages", () => {
    expect(fmt(1.234)).toBe("+1.23%");
    expect(fmt(-0.4)).toBe("−0.40%");
    expect(fmt(0)).toBe("0.00%");
    expect(fmt(-0.001)).toBe("0.00%");
    expect(fmt(212.4)).toBe("+212%");
    expect(fmt(undefined)).toBe("—");
    expect(fmt(61.25, 1)).toBe("+61.3%");
  });
  test("weights", () => {
    expect(weight(1420)).toBe("14.2%");
    expect(weight(60)).toBe("0.60%");
  });
});

describe("squarify", () => {
  const items = [40, 25, 15, 10, 6, 4].map((v, i) => ({ id: i, v }));
  const rects = squarify(items, 0, 0, 600, 400);

  test("every item gets area in proportion to its value", () => {
    expect(rects).toHaveLength(items.length);
    for (const r of rects) expect(r.w * r.h).toBeCloseTo((r.v / 100) * 600 * 400, 3);
  });
  test("tiles fill the box without overlapping", () => {
    const area = rects.reduce((s, r) => s + r.w * r.h, 0);
    expect(area).toBeCloseTo(600 * 400, 3);
    for (const r of rects) {
      expect(r.x).toBeGreaterThanOrEqual(-1e-9);
      expect(r.y).toBeGreaterThanOrEqual(-1e-9);
      expect(r.x + r.w).toBeLessThanOrEqual(600 + 1e-9);
      expect(r.y + r.h).toBeLessThanOrEqual(400 + 1e-9);
    }
    for (let i = 0; i < rects.length; i++)
      for (let j = i + 1; j < rects.length; j++) {
        const a = rects[i];
        const b = rects[j];
        const ox = Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x);
        const oy = Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y);
        expect(ox > 1e-6 && oy > 1e-6).toBe(false);
      }
  });
  test("keeps each item's own fields", () => {
    expect(rects.map((r) => r.id).sort()).toEqual(items.map((i) => i.id));
  });
  test("leaves out items of no value, and draws nothing in no space", () => {
    expect(squarify([{ v: 0 }, { v: 5 }], 0, 0, 10, 10)).toHaveLength(1);
    expect(squarify(items, 0, 0, 0, 100)).toHaveLength(0);
    expect(squarify([], 0, 0, 10, 10)).toHaveLength(0);
  });
});

describe("label", () => {
  test("a big tile shows the ticker and the move", () => {
    const l = label("AAPL", 200, 120);
    expect(l.sym && l.pct).toBe(true);
    expect(l.symPx).toBeLessThanOrEqual(30);
  });
  test("a flat tile shows the move only", () => {
    const l = label("AAPL", 80, 20);
    expect(l.sym).toBe(false);
    expect(l.pct).toBe(true);
  });
  test("a sliver shows colour alone", () => {
    const l = label("AAPL", 12, 12);
    expect(l.sym || l.pct).toBe(false);
  });
});

describe("spark", () => {
  test("spreads points across the box, with 0 % inside the range", () => {
    const s = spark([0, 10, -10, 5], 300, 100, 0);
    expect(s?.line).toBe("M0,50L100,0L200,100L300,25");
    expect(s?.zeroY).toBe(50);
    expect(s?.area).toBe("M0,50L0,50L100,0L200,100L300,25L300,50Z");
  });
  test("a rise all month puts 0 % at the bottom", () => {
    expect(spark([0, 5, 10], 100, 100, 0)?.zeroY).toBe(100);
  });
  test("a flat month is a line, not a division by zero", () => {
    expect(spark([0, 0], 100, 100, 0)?.line).toBe("M0,100L100,100");
  });
  test("nothing to draw with under two points", () => {
    expect(spark([0], 100, 100)).toBeUndefined();
  });
});

describe("spanLabel", () => {
  const day = 86_400_000;
  const now = Date.UTC(2026, 10, 13, 15);
  test("a full month says so", () => {
    expect(spanLabel(now - 30 * day, now)).toBe("past month");
  });
  test("a month still filling in says when it starts", () => {
    expect(spanLabel(Date.UTC(2026, 10, 3), now, "en-GB")).toBe("since 3 Nov");
  });
});
