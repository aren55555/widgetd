import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";

import {
  DistanceUnit,
  HourCycle,
  MassUnit,
  PreferencesSchema,
  resolvePrefs,
  SpeedUnit,
  TemperatureUnit,
  type DisplayPrefs,
} from "../src/prefs.ts";

const detect = (language: string, timeZone = "America/Toronto") => resolvePrefs(undefined, { language, timeZone });
type Units = Pick<DisplayPrefs, "temperature" | "speed" | "mass" | "distance">;
const units = (language: string): Units => {
  const { temperature, speed, mass, distance } = detect(language).prefs;
  return { temperature, speed, mass, distance };
};

const metric: Units = { temperature: TemperatureUnit.CELSIUS, speed: SpeedUnit.KPH, mass: MassUnit.KG, distance: DistanceUnit.KM };
const us: Units = { temperature: TemperatureUnit.FAHRENHEIT, speed: SpeedUnit.MPH, mass: MassUnit.LB, distance: DistanceUnit.MI };

describe("units from the region", () => {
  test("CA is metric", () => expect(units("en-CA")).toEqual(metric));
  test("US is US customary", () => expect(units("en-US")).toEqual(us));
  test("GB: °C, mph, kg, mi", () =>
    expect(units("en-GB")).toEqual({ temperature: TemperatureUnit.CELSIUS, speed: SpeedUnit.MPH, mass: MassUnit.KG, distance: DistanceUnit.MI }));
  test("a bare language takes its likely region (en → US)", () => expect(units("en")).toEqual(us));
  test("fr → FR, metric", () => expect(units("fr")).toEqual(metric));
  test("-u-rg- overrides the region", () => expect(units("en-US-u-rg-cazzzz")).toEqual(metric));
  test("-u-ms-ussystem", () => expect(units("en-CA-u-ms-ussystem")).toEqual(us));
  test("-u-ms-metric", () => expect(units("en-US-u-ms-metric")).toEqual(metric));
  test("-u-mu-fahrenh changes only temperature", () =>
    expect(units("en-CA-u-mu-fahrenh")).toEqual({ ...metric, temperature: TemperatureUnit.FAHRENHEIT }));

  test("units name their evidence", () => {
    expect(detect("en-CA").sources.temperature).toEqual({ kind: "detected", from: "Canada" });
    expect(detect("en-CA-u-ms-ussystem").sources.speed).toEqual({ kind: "detected", from: "-u-ms-ussystem" });
  });
});

describe("locale, zone and clock", () => {
  test("detected from the display", () => {
    const r = detect("en-CA");
    expect(r.prefs.locale).toBe("en-CA");
    expect(r.prefs.timeZone).toBe("America/Toronto");
    expect(r.sources.locale).toEqual({ kind: "detected", from: "browser" });
    expect(r.sources.timeZone).toEqual({ kind: "detected", from: "browser" });
  });

  test("nothing reported: defaults", () => {
    const r = resolvePrefs(undefined, {});
    expect(r.prefs).toMatchObject({ locale: "en-US", timeZone: "UTC", ...us });
    expect(Object.values(r.sources).every((s) => s.kind === "default")).toBe(true);
  });

  test("a bad time zone falls back", () => {
    expect(detect("en-CA", "Mars/Olympus").prefs.timeZone).toBe("UTC");
    const set = resolvePrefs(create(PreferencesSchema, { timeZone: "Nowhere/Land" }), { timeZone: "Europe/Paris" });
    expect(set.prefs.timeZone).toBe("Europe/Paris");
    expect(set.sources.timeZone.kind).toBe("detected");
  });

  test("a bad locale falls back", () => {
    const r = resolvePrefs(create(PreferencesSchema, { locale: "not a locale!" }), { language: "de-DE" });
    expect(r.prefs.locale).toBe("de-DE");
  });

  test("hour cycle from the locale", () => {
    expect(detect("en-US").prefs.hourCycle).toBe(HourCycle.H12);
    expect(detect("en-GB").prefs.hourCycle).toBe(HourCycle.H24);
    expect(detect("de-DE").prefs.hourCycle).toBe(HourCycle.H24);
    expect(detect("en-US-u-hc-h23").prefs.hourCycle).toBe(HourCycle.H24);
  });
});

describe("set values win", () => {
  test("each set field is kept and says so", () => {
    const r = resolvePrefs(
      create(PreferencesSchema, {
        locale: "en-US",
        timeZone: "Asia/Tokyo",
        hourCycle: HourCycle.H24,
        speed: SpeedUnit.MPS,
      }),
      { language: "fr-FR", timeZone: "Europe/Paris" },
    );
    expect(r.prefs).toMatchObject({ locale: "en-US", timeZone: "Asia/Tokyo", hourCycle: HourCycle.H24, speed: SpeedUnit.MPS });
    expect(r.prefs.temperature).toBe(TemperatureUnit.FAHRENHEIT); // from the set locale's region
    expect(r.sources.speed).toEqual({ kind: "set" });
    expect(r.sources.temperature.kind).toBe("detected");
  });

  test("an enum value this build doesn't know counts as unset", () => {
    const r = resolvePrefs(create(PreferencesSchema, { speed: 42 as SpeedUnit }), { language: "en-CA" });
    expect(r.prefs.speed).toBe(SpeedUnit.KPH);
  });
});
