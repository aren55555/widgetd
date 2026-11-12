import { describe, expect, test } from "bun:test";
import { creditLine, halfRange, lineColors, spread, startLabel, teamColor } from "./game.ts";

const tz = "America/Los_Angeles";
const at = (iso: string) => Date.parse(iso);

describe("startLabel", () => {
  const now = at("2026-11-08T18:00:00Z"); // Sun 10:00 AM in LA
  test("today, by time of day", () => {
    expect(startLabel(at("2026-11-08T21:05:00Z"), now, tz)).toBe("Today 1:05 PM");
    expect(startLabel(at("2026-11-09T03:00:00Z"), now, tz)).toBe("Tonight 7:00 PM");
  });
  test("tomorrow, this week, later", () => {
    expect(startLabel(at("2026-11-09T21:00:00Z"), now, tz)).toBe("Tomorrow 1:00 PM");
    expect(startLabel(at("2026-11-12T03:30:00Z"), now, tz)).toBe("Wed 7:30 PM");
    expect(startLabel(at("2026-11-30T21:00:00Z"), now, tz)).toBe("Nov 30, 1:00 PM");
  });
});

test("teamColor", () => {
  expect(teamColor("aa0000")).toBe("#aa0000");
  expect(teamColor("")).toBeUndefined();
  expect(teamColor("red")).toBeUndefined();
});

describe("chart", () => {
  test("lineColors: the away team gives way when the colours clash", () => {
    // Sharks teal and Islanders blue are too alike: the Islanders go orange.
    expect(
      lineColors({ color: "00529b", alternateColor: "f47d31" }, { color: "00788a", alternateColor: "070707" }),
    ).toEqual({
      away: "#f47d31",
      home: "#00788a",
    });
    // Unlike colours stay; near-black is lifted to read on the dark card.
    expect(lineColors({ color: "ef3b24", alternateColor: "" }, { color: "000000", alternateColor: "" })).toEqual({
      away: "#ef3b24",
      home: "color-mix(in oklab, #000000 65%, white)",
    });
    // No colour: the theme's ink.
    expect(lineColors({ color: "", alternateColor: "" }, { color: "00788a", alternateColor: "" }).away).toContain(
      "--wd-ink-muted",
    );
  });

  test("halfRange ignores a decided game's prices", () => {
    expect(halfRange([43, 71, 55], [57, 29, 45])).toBeCloseTo(23.1);
    expect(halfRange([51, 99], [49, 1])).toBe(8); // the jump to 99/1 doesn't count
    expect(halfRange([50], [50])).toBe(8);
    expect(halfRange([96], [4])).toBe(50);
  });

  test("spread pushes close tags apart about their midpoint", () => {
    expect(spread([50, 54], 18)).toEqual([43, 61]);
    expect(spread([54, 50], 18)).toEqual([61, 43]);
    expect(spread([10, 90], 18)).toEqual([10, 90]);
  });
});

test("creditLine", () => {
  const c = (name: string, seasonTotal = 0) => ({ name, seasonTotal });
  expect(creditLine(c("Celebrini", 5), [c("Smith", 8), c("Misa", 2)])).toBe("Celebrini (5); Smith (8) and Misa (2)");
  expect(creditLine(c("Celebrini", 5), [c("Smith", 8)])).toBe("Celebrini (5); Smith (8)");
  expect(creditLine(c("Celebrini", 5), [])).toBe("Celebrini (5)"); // unassisted
  expect(creditLine(c("Stenberg"), [])).toBe("Stenberg"); // a shootout goal: no total
  expect(creditLine(c("A", 1), [c("B", 2), c("C", 3), c("D", 4)])).toBe("A (1); B (2), C (3) and D (4)");
  expect(creditLine(undefined, [])).toBe("");
});
