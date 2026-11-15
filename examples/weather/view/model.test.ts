import { create } from "@bufbuild/protobuf";
import { describe, expect, test } from "bun:test";

import { DistanceUnit, HourCycle, MassUnit, SpeedUnit, TemperatureUnit, type DisplayPrefs } from "@widgetd/sdk/prefs";

import { Condition, DaySchema, StateSchema, Units, type Day } from "./gen/weather/v1/weather_pb.ts";
import {
  along,
  aqiBand,
  clock,
  compass,
  compassName,
  dayName,
  dayTitle,
  degrees,
  FULL_BAR_MM,
  iconFor,
  placeOf,
  rain,
  speed,
  speedIn,
  sway,
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
    expect(speedIn(10, f)).toBe(22);
    expect(rain(9.4, f)).toBe("0.37 in");
    expect(rain(9.4, c)).toBe("9.4 mm");
    expect(rain(0, c)).toBe("None");
  });

  test("compass points", () => {
    expect([0, 44, 46, 180, 225, 359, 360, -90].map(compass)).toEqual(["N", "NE", "NE", "S", "SW", "N", "N", "W"]);
    expect(compassName(225)).toBe("southwest");
  });

  test("the needle sways with the gusts", () => {
    expect(sway(0.2, 5)).toBe(0); // calm
    expect(sway(5, 5)).toBe(2); // steady
    expect(sway(5, 10)).toBe(12);
    expect(sway(2, 20)).toBe(15); // capped
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
  const berlin = { lat: 52.52, lon: 13.41 };
  // 240×58: the strip's top is y 4, the horizon y 42.6 (sin 0), its bottom sin −0.4.
  const W = 240;
  const H = 58;

  test("the sun's track is its height in the sky: horizon at sunrise, highest near noon", () => {
    const m = strip(sat, h(2026, 10, 24, 11), W, H, berlin); // 13:00 in Berlin
    const track = m.sun!;
    expect(track.horizon).toBeCloseTo(4 + 54 / 1.4, 5);
    expect(track.path.startsWith("M0.0 ")).toBe(true);
    expect(track.area.startsWith(track.path)).toBe(true);
    // Late October in Berlin: about 25° at solar noon, which is near 12:55 CEST.
    expect(track.peak!.deg).toBeGreaterThan(24);
    expect(track.peak!.deg).toBeLessThan(26);
    expect(Math.abs(track.peak!.t - h(2026, 10, 24, 10, 55))).toBeLessThan(10 * 60);
    expect(track.peak!.y).toBeCloseTo(
      track.horizon - Math.sin((track.peak!.deg * Math.PI) / 180) * (track.horizon - 4),
      5,
    );
    // The sky: dark at midnight, light at noon, glowing near sunrise.
    expect(m.sky).toHaveLength(145);
    expect(m.sky[0].light).toBe(0);
    expect(m.sky[78].light).toBeGreaterThan(0.6); // 13:00
    expect(Math.max(...m.sky.slice(40, 52).map((x) => x.glow))).toBeGreaterThan(0.9); // 06:40–08:40
    expect(m.now!.left).toBeCloseTo((13 / 24) * 100, 5);
    expect(m.now!.y!).toBeLessThan(track.horizon);
    // After dark the sun is still tracked, below the horizon.
    const late = strip(sat, h(2026, 10, 24, 20), W, H, berlin).now!;
    expect(late.deg!).toBeLessThan(-30);
    expect(late.y!).toBeGreaterThan(track.horizon);
    expect(strip(sun, h(2026, 10, 24, 11), W, H, berlin).now).toBeNull(); // not today
  });

  test("no place, no sun", () => {
    const m = strip(sat, h(2026, 10, 24, 11), W, H, null);
    expect(m.sun).toBeNull();
    expect(m.sky).toEqual([]);
    expect(m.now!.y).toBeNull();
  });

  test("placeOf needs both coordinates", () => {
    expect(placeOf(create(StateSchema, { latitude: 52.52, longitude: 13.41 }))).toEqual(berlin);
    expect(placeOf(create(StateSchema, { latitude: 52.52 }))).toBeNull();
    // 0, 0 is a place (in the Gulf of Guinea), not a missing one.
    expect(placeOf(create(StateSchema, { latitude: 0, longitude: 0 }))).toEqual({ lat: 0, lon: 0 });
  });

  test("a 25-hour day divides into 25 hours", () => {
    const m = strip(sun, 0, 240, 60, null);
    expect(m.bars).toHaveLength(2);
    expect(m.bars[0].left).toBeCloseTo((9 / 25) * 100 + (100 / 25) * 0.12, 5);
    // Height is the amount, clipped; opacity the chance.
    expect(m.bars[0].height).toBe(72);
    expect(m.bars[1].height).toBeCloseTo((1 / FULL_BAR_MM) * 72, 5);
    expect(m.bars[0].opacity).toBe(1);
    expect(m.bars[1].opacity).toBe(0.25);
    expect(along(sun, Number(sun.start) + 12.5 * 3600)).toBe(50);
  });
});
