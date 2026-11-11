// Board-side helpers around the display preferences: the readable rows of the
// settings modal and the offline badge's text. The resolution itself
// (resolvePrefs) lives in the SDK, which runs the same detection when a
// board sends no preferences.

import {
  DistanceUnit,
  HourCycle,
  MassUnit,
  SpeedUnit,
  TemperatureUnit,
  type DisplayPrefs,
  type ResolvedPrefs,
  type Source,
} from "@widgetd/sdk/prefs";

import type { Layout, LayoutSource } from "./options.ts";

export interface Row {
  label: string;
  value: string;
  source: string;
}

export const sourceText = (s: Source | LayoutSource): string => {
  if (typeof s === "string") return s;
  return s.kind === "detected" ? `detected · ${s.from}` : s.kind;
};

function languageName(locale: string): string {
  try {
    const base = new Intl.Locale(locale).baseName;
    const name = new Intl.DisplayNames([locale], { type: "language" }).of(base);
    return name && name !== base ? `${name} (${locale})` : locale;
  } catch {
    return locale;
  }
}

/** "30 s", "2 min", "1 h 30 min". */
export function durationText(ms: number): string {
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s} s`;
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const rest = s % 60;
  const parts = [];
  if (h) parts.push(`${h} h`);
  if (m) parts.push(`${m} min`);
  if (rest) parts.push(`${rest} s`);
  return parts.join(" ");
}

const clock = (h: DisplayPrefs["hourCycle"]) => {
  switch (h) {
    case HourCycle.H12:
      return "12-hour";
    case HourCycle.H24:
      return "24-hour";
  }
};
const temperature = (t: DisplayPrefs["temperature"]) => {
  switch (t) {
    case TemperatureUnit.CELSIUS:
      return "Celsius (°C)";
    case TemperatureUnit.FAHRENHEIT:
      return "Fahrenheit (°F)";
  }
};
const speed = (s: DisplayPrefs["speed"]) => {
  switch (s) {
    case SpeedUnit.KPH:
      return "km/h";
    case SpeedUnit.MPH:
      return "mph";
    case SpeedUnit.MPS:
      return "m/s";
  }
};
const mass = (m: DisplayPrefs["mass"]) => {
  switch (m) {
    case MassUnit.KG:
      return "kg";
    case MassUnit.LB:
      return "lb";
  }
};
const distance = (d: DisplayPrefs["distance"]) => {
  switch (d) {
    case DistanceUnit.KM:
      return "km";
    case DistanceUnit.MI:
      return "mi";
  }
};

export function layoutRows(l: Layout): Row[] {
  return [
    { label: "Columns", value: String(l.cols), source: sourceText(l.colsSource) },
    { label: "Offline badge after", value: durationText(l.staleAfterMs), source: sourceText(l.staleAfterSource) },
  ];
}

export function prefsRows({ prefs: p, sources: s }: ResolvedPrefs): Row[] {
  return [
    { label: "Language & format", value: languageName(p.locale), source: sourceText(s.locale) },
    { label: "Time zone", value: p.timeZone, source: sourceText(s.timeZone) },
    { label: "Clock", value: clock(p.hourCycle), source: sourceText(s.hourCycle) },
    { label: "Temperature", value: temperature(p.temperature), source: sourceText(s.temperature) },
    { label: "Speed", value: speed(p.speed), source: sourceText(s.speed) },
    { label: "Weight", value: mass(p.mass), source: sourceText(s.mass) },
    { label: "Distance", value: distance(p.distance), source: sourceText(s.distance) },
  ];
}

const dayKey = (ms: number, timeZone: string) =>
  new Intl.DateTimeFormat("en-CA", { timeZone, year: "numeric", month: "2-digit", day: "2-digit" }).format(ms);

/** "Offline · last update 14:32", with the date when that was another day. */
export function offlineText(lastLive: number | undefined, now: number, p: DisplayPrefs): string {
  if (lastLive === undefined) return "Offline";
  const sameDay = dayKey(lastLive, p.timeZone) === dayKey(now, p.timeZone);
  const f = new Intl.DateTimeFormat(p.locale, {
    timeZone: p.timeZone,
    hour: "numeric",
    minute: "2-digit",
    hourCycle: p.hourCycle === HourCycle.H24 ? "h23" : "h12",
    ...(sameDay ? {} : { month: "short", day: "numeric" }),
  });
  return `Offline · last update ${f.format(lastLive)}`;
}

/** The display app's version, from the `widgetd-display/<version>` user-agent suffix. */
export function appVersion(userAgent: string): string | undefined {
  return /\bwidgetd-display\/(\S+)/.exec(userAgent)?.[1];
}
