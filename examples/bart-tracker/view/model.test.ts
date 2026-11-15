import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { HourCycle, type DisplayPrefs } from "@widgetd/sdk";

import { DepartureSchema, TrainSchema } from "./gen/barttracker/v1/barttracker_pb.ts";
import {
  clock,
  drumSteps,
  inkFor,
  minutesUntil,
  shortName,
  standing,
  trainLabel,
  trainPosition,
  upcoming,
} from "./model.ts";

const NOW = 1_800_000_000_000;

describe("trainPosition", () => {
  // Between stations 2 and 3, a 300 s stretch, due at 3 in 100 s: two thirds of the way.
  const t = create(TrainSchema, { fromIndex: 2, toIndex: 3, stretchSeconds: 300, arriveUnixMs: BigInt(NOW + 100_000) });

  test("by the share of the stretch's time that has passed", () => {
    expect(trainPosition(t, NOW)).toBeCloseTo(2 + 2 / 3);
    expect(trainPosition(t, NOW - 200_000)).toBe(2); // not yet started: at the start
    expect(trainPosition(t, NOW + 500_000)).toBe(3); // overdue: at the station
  });

  test("runs the other way too", () => {
    const back = create(TrainSchema, { ...t, fromIndex: 3, toIndex: 2 });
    expect(trainPosition(back, NOW)).toBeCloseTo(3 - 2 / 3);
  });

  test("standing at a station", () => {
    const at = create(TrainSchema, { fromIndex: 4, toIndex: 4, arriveUnixMs: BigInt(NOW - 5000) });
    expect(trainPosition(at, NOW)).toBe(4);
    expect(standing(at, NOW)).toBe(true);
    expect(standing(t, NOW)).toBe(false);
  });
});

describe("departures", () => {
  const dep = (leavesIn: number, trip: string) =>
    create(DepartureSchema, { trip, leavesUnixMs: BigInt(NOW + leavesIn) });

  test("drop trains 30 s after they leave, and show at most three", () => {
    const shown = upcoming(
      [dep(-40_000, "gone"), dep(-10_000, "just"), dep(60_000, "a"), dep(120_000, "b"), dep(180_000, "c")],
      NOW,
    );
    expect(shown.map((d) => d.trip)).toEqual(["just", "a", "b"]);
  });

  test("count down in whole minutes, never below zero", () => {
    expect(minutesUntil(NOW + 89_000, NOW)).toBe(1);
    expect(minutesUntil(NOW + 91_000, NOW)).toBe(2);
    expect(minutesUntil(BigInt(NOW - 60_000), NOW)).toBe(0);
  });
});

test("trainLabel", () => {
  const t = create(TrainSchema, {
    destination: "Antioch",
    fromIndex: 6,
    toIndex: 7,
    stretchSeconds: 300,
    arriveUnixMs: BigInt(NOW + 240_000),
  });
  expect(trainLabel(t, "Orinda", NOW)).toBe("Antioch · next Orinda in 4 min");
  expect(trainLabel(t, "Orinda", NOW + 300_000)).toBe("Antioch · at Orinda");
});

test("shortName", () => {
  expect(shortName("Montgomery Street")).toBe("Montgomery");
  expect(shortName("12th Street / Oakland City Center")).toBe("12th St");
  expect(shortName("Powell Street")).toBe("Powell St");
  expect(shortName("Lafayette")).toBe("Lafayette");
});

test("inkFor", () => {
  expect(inkFor("FFFF33")).toBe("#18181b"); // yellow: dark ink
  expect(inkFor("FF0000")).toBe("#ffffff"); // red: white
  expect(inkFor("0099CC")).toBe("#ffffff"); // blue: white
});

describe("drumSteps", () => {
  test("turn forward through every character between", () => {
    expect(drumSteps("2", "8")).toEqual(["3", "4", "5", "6", "7", "8"]);
    expect(drumSteps("8", "2")).toEqual(["9", " ", "0", "1", "2"]);
    expect(drumSteps(" ", "1")).toEqual(["0", "1"]);
  });

  test("nothing to do, or nothing on the drum", () => {
    expect(drumSteps("5", "5")).toEqual([]);
    expect(drumSteps("5", "x")).toEqual(["x"]);
  });
});

describe("clock", () => {
  const prefs = (hourCycle: HourCycle, timeZone = "America/Los_Angeles") =>
    ({ locale: "en-US", timeZone, hourCycle }) as DisplayPrefs;
  // 2027-01-15 21:05 UTC is 1:05 PM in San Francisco.
  const at = Date.UTC(2027, 0, 15, 21, 5);

  test("in the screen's time zone and clock", () => {
    expect(clock(at, prefs(HourCycle.H12))).toEqual({ short: "1:05", full: "1:05 PM" });
    expect(clock(at, prefs(HourCycle.H24))).toEqual({ short: "13:05", full: "13:05" });
    expect(clock(at, prefs(HourCycle.H24, "Europe/Paris")).short).toBe("22:05");
  });
});
