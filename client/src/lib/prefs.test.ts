import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { HourCycle, PreferencesSchema, resolvePrefs } from "@widgetd/sdk/prefs";

import { appVersion, durationText, offlineText, prefsRows } from "./prefs.ts";
import { CardStore, LIVE_EVERY, type KVBackend } from "./store.ts";

const prefs = (hourCycle: HourCycle) =>
  resolvePrefs(create(PreferencesSchema, { locale: "en-CA", timeZone: "America/Toronto", hourCycle }), {}).prefs;

describe("offlineText", () => {
  // 2026-11-10 14:32 in Toronto (UTC-5).
  const at = Date.UTC(2026, 10, 10, 19, 32);

  test("same day: the time only, in the screen's zone and clock", () => {
    expect(offlineText(at, at + 60_000, prefs(HourCycle.H24))).toBe("Offline · last update 14:32");
    expect(offlineText(at, at + 60_000, prefs(HourCycle.H12))).toMatch(/^Offline · last update 2:32\s(PM|p\.m\.)$/i);
  });

  test("another day: with the date", () => {
    expect(offlineText(at, at + 86_400_000, prefs(HourCycle.H24))).toMatch(/^Offline · last update Nov 10\b.*\b14:32$/);
  });

  test("never live", () => {
    expect(offlineText(undefined, at, prefs(HourCycle.H24))).toBe("Offline");
  });
});

describe("settings rows", () => {
  test("values and sources", () => {
    const r = resolvePrefs(create(PreferencesSchema, { timeZone: "Asia/Tokyo" }), { language: "en-CA" });
    const rows = Object.fromEntries(prefsRows(r).map((x) => [x.label, x]));
    expect(rows["Language & format"]).toEqual({
      label: "Language & format",
      value: "Canadian English (en-CA)",
      source: "detected · browser",
    });
    expect(rows["Time zone"]).toMatchObject({ value: "Asia/Tokyo", source: "set" });
    expect(rows.Temperature).toMatchObject({ value: "Celsius (°C)", source: "detected · Canada" });
    expect(rows.Speed.value).toBe("km/h");
  });

  test("durationText", () => {
    expect(durationText(30_000)).toBe("30 s");
    expect(durationText(120_000)).toBe("2 min");
    expect(durationText(5_430_000)).toBe("1 h 30 min 30 s");
  });

  test("appVersion", () => {
    expect(appVersion("Mozilla/5.0 (Linux; Android 14) Chrome/130 Mobile widgetd-display/1.2.3")).toBe("1.2.3");
    expect(appVersion("Mozilla/5.0 Chrome/130")).toBeUndefined();
  });
});

describe("lastLive", () => {
  test("written at most once per LIVE_EVERY", () => {
    const m = new Map<string, string>();
    const writes: string[] = [];
    const ls: KVBackend = {
      getItem: (k) => m.get(k) ?? null,
      setItem: (k, v) => void (writes.push(v), m.set(k, v)),
      removeItem: (k) => void m.delete(k),
      key: (i) => [...m.keys()][i] ?? null,
      get length() {
        return m.size;
      },
    };
    const s = new CardStore(ls);
    expect(s.lastLive()).toBeUndefined();
    s.markLive(1000);
    s.markLive(1000 + LIVE_EVERY - 1);
    expect(s.lastLive()).toBe(1000);
    s.markLive(1000 + LIVE_EVERY);
    expect(s.lastLive()).toBe(1000 + LIVE_EVERY);
    expect(writes).toHaveLength(2);
    expect(m.has("widgetd:lastLive")).toBe(true);
  });
});
