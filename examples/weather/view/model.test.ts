import { create } from "@bufbuild/protobuf";
import { describe, expect, test } from "bun:test";

import { DistanceUnit, HourCycle, MassUnit, SpeedUnit, TemperatureUnit, type DisplayPrefs } from "@widgetd/sdk/prefs";

import { Condition, DaySchema, StateSchema, Units, type Day } from "./gen/weather/v1/weather_pb.ts";
import {
  along,
  aqiBand,
  clock,
  compass,
  dayName,
  dayTitle,
  degrees,
  FULL_BAR_MM,
  iconFor,
  rain,
  speed,
  strip,
  TINTS,
  unitsFor,
  upcoming,
} from "./model.ts";

const US: DisplayPrefs = {
  locale: "en-US",
  timeZone: "America/Chicago",
  hourCycle: HourCycle.H12,
  temperature: TemperatureUnit.FAHRENHEIT,
  speed: SpeedUnit.MPH,
  mass: MassUnit.LB,
  distance: DistanceUnit.MI,
};
const DE: DisplayPrefs = {
  locale: "de-DE",
  timeZone: "Europe/Berlin",
  hourCycle: HourCycle.H24,
  temperature: TemperatureUnit.CELSIUS,
  speed: SpeedUnit.KPH,
  mass: MassUnit.KG,
  distance: DistanceUnit.KM,
};
const BERLIN = "Europe/Berlin";
const h = (y: number, mo: number, d: number, hr: number, min = 0) => Date.UTC(y, mo - 1, d, hr, min) / 1000;

// 24 and 25 Oct 2026 in Berlin; the clocks go back on the 25th.
const sat: Day = create(DaySchema, {
  start: BigInt(h(2026, 10, 23, 22)),
  end: BigInt(h(2026, 10, 24, 22)),
  condition: Condition.CLEAR,
  sunrise: BigInt(h(2026, 10, 24, 5, 47)),
  sunset: BigInt(h(2026, 10, 24, 15, 59)),
  hourlyPrecipMm: Array<number>(24).fill(0),
  hourlyPrecipChance: Array<number>(24).fill(5),
});
const sun: Day = create(DaySchema, {
  start: BigInt(h(2026, 10, 24, 22)),
  end: BigInt(h(2026, 10, 25, 23)),
  condition: Condition.RAIN,
  sunrise: BigInt(h(2026, 10, 25, 5, 49)),
  sunset: BigInt(h(2026, 10, 25, 15, 57)),
  hourlyPrecipMm: Array.from({ length: 25 }, (_, i) => (i === 9 ? 3 : i === 10 ? 1 : 0)),
  hourlyPrecipChance: Array.from({ length: 25 }, (_, i) => (i === 9 ? 100 : i === 10 ? 0 : 5)),
});

describe("units", () => {
  test("the card's override beats the screen, for temperature only", () => {
    expect(unitsFor(Units.SCREEN_SETTING, US)).toMatchObject({ temp: "F", speed: "mph", precip: "in", hour12: true });
    expect(unitsFor(Units.SCREEN_SETTING, DE)).toMatchObject({ temp: "C", speed: "km/h", precip: "mm", hour12: false });
    expect(unitsFor(Units.CELSIUS, US)).toMatchObject({ temp: "C", speed: "mph", precip: "in" });
    expect(unitsFor(Units.FAHRENHEIT, DE)).toMatchObject({ temp: "F", speed: "km/h", precip: "mm" });
  });

  test("conversions", () => {
    const f = unitsFor(Units.SCREEN_SETTING, US);
    const c = unitsFor(Units.SCREEN_SETTING, DE);
    expect(degrees(17, f)).toBe("63°");
    expect(degrees(-40, f)).toBe("-40°");
    expect(degrees(16.6, c)).toBe("17°");
    expect(speed(10, f)).toBe("22 mph");
    expect(speed(10, c)).toBe("36 km/h");
    expect(speed(10, { ...c, speed: "m/s" })).toBe("10 m/s");
    expect(rain(9.4, f)).toBe("0.37 in");
    expect(rain(9.4, c)).toBe("9.4 mm");
    expect(rain(0, c)).toBe("None");
  });

  test("compass points", () => {
    expect([0, 44, 46, 180, 225, 359, 360, -90].map(compass)).toEqual(["N", "NE", "NE", "S", "SW", "N", "N", "W"]);
  });
});

describe("icons and air", () => {
  test("night has a moon", () => {
    expect(iconFor(Condition.CLEAR)).toBe("sun");
    expect(iconFor(Condition.CLEAR, false)).toBe("moon");
    expect(iconFor(Condition.PARTLY_CLOUDY, false)).toBe("cloudMoon");
    expect(iconFor(Condition.UNSPECIFIED)).toBe("cloud");
  });

  test("tints colour the sun and what falls, never the cloud", () => {
    expect(TINTS.cloudSun[5]).toBeUndefined();
    expect(TINTS.rain[0]).toBeUndefined();
    expect(TINTS.rain.slice(1)).toEqual(["wet", "wet", "wet"]);
  });

  test("EPA bands", () => {
    expect([0, 50, 51, 100, 101, 150, 151, 200, 201, 300, 301, 500].map((v) => aqiBand(v).key)).toEqual([
      "good",
      "good",
      "moderate",
      "moderate",
      "sensitive",
      "sensitive",
      "unhealthy",
      "unhealthy",
      "very",
      "very",
      "hazardous",
      "hazardous",
    ]);
  });
});

describe("days", () => {
  const u = unitsFor(Units.SCREEN_SETTING, US);
  const st = create(StateSchema, { name: "Berlin", timeZone: BERLIN, days: [sat, sun] });

  test("named at the place, not the screen", () => {
    const sat9 = h(2026, 10, 24, 7); // 09:00 Saturday in Berlin
    expect(dayName(sat, sat9, BERLIN, u)).toBe("Today");
    expect(dayName(sun, sat9, BERLIN, u)).toBe("Sun");
    expect(dayTitle(sun, sat9, BERLIN, u)).toBe("Sun, Oct 25");
    // 23:30 Saturday in Berlin is already Sunday in UTC: still Saturday here.
    expect(dayName(sat, h(2026, 10, 24, 21, 30), BERLIN, u)).toBe("Today");
  });

  test("a stale state drops the days that have ended", () => {
    expect(upcoming(st, h(2026, 10, 24, 7))).toHaveLength(2);
    expect(upcoming(st, h(2026, 10, 24, 23))).toEqual([sun]);
    expect(upcoming(st, h(2026, 10, 26, 0))).toEqual([]);
  });

  test("clock follows the screen's hour cycle, in the place's zone", () => {
    // 05:47 UTC is 07:47 in Berlin (summer time, UTC+2).
    expect(clock(Number(sat.sunrise), BERLIN, u)).toBe("7:47 AM");
    expect(clock(Number(sat.sunrise), BERLIN, unitsFor(Units.SCREEN_SETTING, DE))).toBe("07:47");
  });
});

describe("strip", () => {
  test("daylight and the sun's arc sit between sunrise and sunset", () => {
    const m = strip(sat, h(2026, 10, 24, 11), 240, 60); // 13:00 in Berlin
    expect(m.daylight!.left).toBeCloseTo((7 * 60 + 47) / 14.4, 5);
    expect(m.daylight!.twilight).toBeCloseTo((40 / 1440) * 100, 5);
    expect(m.arc.startsWith("M")).toBe(true);
    // The sunlit area is the arc, closed down to the strip's bottom.
    expect(m.area.startsWith(m.arc)).toBe(true);
    expect(m.area.endsWith("Z")).toBe(true);
    expect(m.horizon!.rise).toBeCloseTo((m.daylight!.left / 100) * 240, 5);
    expect(m.horizon!.y).toBe(57);
    expect(m.horizon!.reach).toBeCloseTo((40 / 1440) * 240, 5);
    expect(m.now!.left).toBeCloseTo((13 / 24) * 100, 5);
    expect(m.now!.y).toBeLessThan(20); // near the top, early afternoon
    expect(strip(sat, h(2026, 10, 24, 20), 240, 60).now!.y).toBeNull(); // after dark
    expect(strip(sun, h(2026, 10, 24, 11), 240, 60).now).toBeNull(); // not today
  });

  test("a 25-hour day divides into 25 hours", () => {
    const m = strip(sun, 0, 240, 60);
    expect(m.bars).toHaveLength(2);
    expect(m.bars[0].left).toBeCloseTo((9 / 25) * 100 + (100 / 25) * 0.12, 5);
    // Height is the amount, clipped; opacity the chance.
    expect(m.bars[0].height).toBe(72);
    expect(m.bars[1].height).toBeCloseTo((1 / FULL_BAR_MM) * 72, 5);
    expect(m.bars[0].opacity).toBe(1);
    expect(m.bars[1].opacity).toBe(0.25);
    expect(along(sun, Number(sun.start) + 12.5 * 3600)).toBe(50);
  });

  test("no sunrise, no arc", () => {
    const polar = create(DaySchema, { ...sat, sunrise: 0n, sunset: 0n });
    const m = strip(polar, h(2026, 10, 24, 11), 240, 60);
    expect(m.arc).toBe("");
    expect(m.area).toBe("");
    expect(m.horizon).toBeNull();
    expect(m.daylight).toBeNull();
    expect(m.now!.y).toBeNull();
  });
});
