import { describe, expect, test } from "bun:test";

import { horizonGlow, skyLight, solarElevation, SUNRISE_ALTITUDE } from "./sun.ts";

const h = (y: number, mo: number, d: number, hr: number, min = 0) => Date.UTC(y, mo - 1, d, hr, min) / 1000;
const BERLIN = [52.52, 13.41] as const;
const SF = [37.7749, -122.4194] as const;

describe("solarElevation", () => {
  test("crosses the sunrise altitude when Open-Meteo's sunrise and sunset say", () => {
    // Open-Meteo's daily sunrise and sunset (to the second), for the grid points it picked.
    const cases = [
      // Berlin, 24–26 Oct 2026.
      { at: [52.512604, 13.419517], times: [1792820870, 1792907379, 1792993887, 1792857141, 1792943418, 1793029696] },
      // San Francisco, 14–17 Nov 2026.
      {
        at: [37.763283, -122.41286],
        times: [1794667743, 1794754206, 1794840669, 1794927131, 1794704331, 1794790688, 1794877047, 1794963408],
      },
    ] as const;
    for (const { at, times } of cases) {
      for (const t of times) {
        // The sun moves about 0.15° a minute near the horizon: this is within half a minute.
        expect(Math.abs(solarElevation(at[0], at[1], t) - SUNRISE_ALTITUDE)).toBeLessThan(0.08);
      }
    }
    expect(solarElevation(...BERLIN, 1792820870 - 600)).toBeLessThan(SUNRISE_ALTITUDE);
    expect(solarElevation(...BERLIN, 1792820870 + 600)).toBeGreaterThan(SUNRISE_ALTITUDE);
  });

  test("noon is as high as latitude and declination allow", () => {
    // At the June solstice the sun stands 90° − (lat − 23.44°) high at solar noon.
    const peak = (lat: number, lon: number, day: number) =>
      Math.max(...Array.from({ length: 1440 }, (_, m) => solarElevation(lat, lon, day + m * 60)));
    expect(peak(...SF, h(2026, 6, 21, 0))).toBeCloseTo(90 - (37.7749 - 23.44), 0);
    // And in December, 23.44° the other way: much lower.
    expect(peak(...SF, h(2026, 12, 21, 0))).toBeCloseTo(90 - (37.7749 + 23.44), 0);
  });

  test("midnight is as low as it gets", () => {
    // San Francisco's solar midnight in June: lat + decl − 90 below.
    expect(solarElevation(...SF, h(2026, 6, 21, 8, 10))).toBeCloseTo(37.7749 + 23.44 - 90, 0);
  });

  test("a polar day keeps the sun up", () => {
    const TROMSO = [69.65, 18.96] as const;
    for (let hr = 0; hr < 24; hr++) expect(solarElevation(...TROMSO, h(2026, 6, 21, hr))).toBeGreaterThan(0);
  });
});

describe("skyLight", () => {
  test("bright by day, dark after astronomical twilight, and never jumps", () => {
    expect(skyLight(90)).toBe(1);
    expect(skyLight(-18)).toBe(0);
    expect(skyLight(-30)).toBe(0);
    expect(skyLight(-1e-9)).toBeCloseTo(skyLight(0), 6);
    expect(skyLight(30)).toBeGreaterThan(skyLight(10));
    expect(skyLight(-6)).toBeGreaterThan(skyLight(-12));
  });

  test("the horizon glows around sunrise and sunset only", () => {
    expect(horizonGlow(-1)).toBe(1);
    expect(horizonGlow(10)).toBe(0);
    expect(horizonGlow(-10)).toBe(0);
  });
});
