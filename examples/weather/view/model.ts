// Everything the weather card draws, worked out without a DOM: unit
// conversions, labels, and the detail view's 24-hour strip. view.ts only
// puts these on screen.

import type { DisplayPrefs } from "@widgetd/sdk/prefs";
import { DistanceUnit, HourCycle, SpeedUnit, TemperatureUnit } from "@widgetd/sdk/prefs";

import { Condition, Units, type Day, type State } from "./gen/weather/v1/weather_pb.ts";
import { horizonGlow, skyLight, solarElevation } from "./sun.ts";

/** How this screen shows the card's quantities. */
export interface Units3 {
  temp: "C" | "F";
  speed: "km/h" | "mph" | "m/s";
  precip: "mm" | "in";
  locale: string;
  hour12: boolean;
}

/** The card's own temperature unit wins; otherwise the screen's. Wind and rain always follow the screen. */
export function unitsFor(override: Units, prefs: DisplayPrefs): Units3 {
  const temp =
    override === Units.CELSIUS
      ? "C"
      : override === Units.FAHRENHEIT
        ? "F"
        : prefs.temperature === TemperatureUnit.FAHRENHEIT
          ? "F"
          : "C";
  const speed = prefs.speed === SpeedUnit.MPH ? "mph" : prefs.speed === SpeedUnit.MPS ? "m/s" : "km/h";
  return {
    temp,
    speed,
    precip: prefs.distance === DistanceUnit.MI ? "in" : "mm",
    locale: prefs.locale,
    hour12: prefs.hourCycle === HourCycle.H12,
  };
}

export const degrees = (c: number, u: Units3): string => `${Math.round(u.temp === "F" ? (c * 9) / 5 + 32 : c)}°`;

export function speed(mps: number, u: Units3): string {
  if (u.speed === "mph") return `${Math.round(mps * 2.236936)} mph`;
  if (u.speed === "m/s") return `${Math.round(mps)} m/s`;
  return `${Math.round(mps * 3.6)} km/h`;
}

export function rain(mm: number, u: Units3): string {
  if (mm < 0.05) return "None";
  return u.precip === "in" ? `${(mm / 25.4).toFixed(2)} in` : `${mm.toFixed(1)} mm`;
}

const POINTS = ["N", "NE", "E", "SE", "S", "SW", "W", "NW"];
/** "SW" for a wind from 225°. */
export const compass = (deg: number): string => POINTS[Math.round((((deg % 360) + 360) % 360) / 45) % 8];

// ── Icons ────────────────────────────────────────────────────────────────

export type IconKey =
  "sun" | "moon" | "cloudSun" | "cloudMoon" | "cloud" | "fog" | "drizzle" | "rain" | "snow" | "storm";

export function iconFor(c: Condition, isDay = true): IconKey {
  switch (c) {
    case Condition.CLEAR:
      return isDay ? "sun" : "moon";
    case Condition.PARTLY_CLOUDY:
      return isDay ? "cloudSun" : "cloudMoon";
    case Condition.FOG:
      return "fog";
    case Condition.DRIZZLE:
      return "drizzle";
    case Condition.RAIN:
      return "rain";
    case Condition.SNOW:
      return "snow";
    case Condition.THUNDERSTORM:
      return "storm";
    case Condition.CLOUDY:
    case Condition.UNSPECIFIED:
    default:
      return "cloud";
  }
}

export function conditionText(c: Condition, isDay = true): string {
  switch (c) {
    case Condition.CLEAR:
      return isDay ? "Sunny" : "Clear";
    case Condition.PARTLY_CLOUDY:
      return "Partly cloudy";
    case Condition.FOG:
      return "Fog";
    case Condition.DRIZZLE:
      return "Drizzle";
    case Condition.RAIN:
      return "Rain";
    case Condition.SNOW:
      return "Snow";
    case Condition.THUNDERSTORM:
      return "Thunderstorms";
    case Condition.CLOUDY:
    case Condition.UNSPECIFIED:
    default:
      return "Cloudy";
  }
}

/** Which colour each of an icon's Lucide shapes takes; unlisted shapes stay ink. Only the sun and what falls carry colour. */
export type Tint = "sun" | "wet" | "ice" | "bolt";
export const TINTS: Record<IconKey, ReadonlyArray<Tint | undefined>> = {
  sun: ["sun", "sun", "sun", "sun", "sun", "sun", "sun", "sun", "sun"],
  moon: [],
  cloudSun: ["sun", "sun", "sun", "sun", "sun"],
  cloudMoon: [],
  cloud: [],
  fog: [],
  drizzle: [undefined, "wet", "wet", "wet", "wet", "wet", "wet"],
  rain: [undefined, "wet", "wet", "wet"],
  snow: [undefined, "ice", "ice", "ice", "ice", "ice", "ice"],
  storm: [undefined, "bolt"],
};

// ── Air quality ──────────────────────────────────────────────────────────

export interface AqiBand {
  key: "good" | "moderate" | "sensitive" | "unhealthy" | "very" | "hazardous";
  label: string;
}

/** The US EPA band an AQI falls in. */
export function aqiBand(v: number): AqiBand {
  if (v <= 50) return { key: "good", label: "Good" };
  if (v <= 100) return { key: "moderate", label: "Moderate" };
  if (v <= 150) return { key: "sensitive", label: "Unhealthy for sensitive groups" };
  if (v <= 200) return { key: "unhealthy", label: "Unhealthy" };
  if (v <= 300) return { key: "very", label: "Very unhealthy" };
  return { key: "hazardous", label: "Hazardous" };
}

// ── Days ─────────────────────────────────────────────────────────────────

/** The days still worth showing: from the one now falls in (the state may be a few hours old). */
export function upcoming(st: State, nowSec: number): Day[] {
  const from = st.days.findIndex((d) => nowSec < Number(d.end));
  return from < 0 ? [] : st.days.slice(from);
}

const fmt = (u: Units3, tz: string, o: Intl.DateTimeFormatOptions) => {
  try {
    return new Intl.DateTimeFormat(u.locale, { ...o, timeZone: tz || "UTC" });
  } catch {
    return new Intl.DateTimeFormat(u.locale, { ...o, timeZone: "UTC" });
  }
};

/** A day's start, named at the place: its midnight, plus a few hours so DST can't push it back a day. */
const noonOf = (d: Day) => (Number(d.start) + 12 * 3600) * 1000;

/** "Today" for the day now falls in, otherwise "Sun". */
export function dayName(d: Day, nowSec: number, tz: string, u: Units3): string {
  if (Number(d.start) <= nowSec && nowSec < Number(d.end)) return "Today";
  return fmt(u, tz, { weekday: "short" }).format(noonOf(d));
}

/** "Mon 16 Nov", or "Today". */
export function dayTitle(d: Day, nowSec: number, tz: string, u: Units3): string {
  if (Number(d.start) <= nowSec && nowSec < Number(d.end)) return "Today";
  return fmt(u, tz, { weekday: "short", day: "numeric", month: "short" }).format(noonOf(d));
}

/** A time of day at the place, on the screen's clock: "6:51 AM" or "06:51". */
export const clock = (sec: number, tz: string, u: Units3): string =>
  fmt(u, tz, { hour: u.hour12 ? "numeric" : "2-digit", minute: "2-digit", hour12: u.hour12 }).format(sec * 1000);

// ── The detail view's strip ──────────────────────────────────────────────

export interface Bar {
  /** Percent of the strip's width. */
  left: number;
  width: number;
  /** Percent of its height: the amount. */
  height: number;
  /** The chance, as opacity. */
  opacity: number;
}

/** Where a place is, in degrees north and east. */
export interface Place {
  lat: number;
  lon: number;
}

/** The place in a state, or null before one is picked (or from a server too old to say). */
export const placeOf = (s: State): Place | null =>
  s.latitude != null && s.longitude != null ? { lat: s.latitude, lon: s.longitude } : null;

/** The sky at one moment, at offset 0..1 along the strip. */
export interface SkyStop {
  offset: number;
  /** skyLight: 0 night to 1 the sun overhead. */
  light: number;
  /** horizonGlow: 0 to 1. */
  glow: number;
}

/** The sun's track through the day, in px over the strip's box. */
export interface SunTrack {
  /** Its elevation all day, above the horizon and below it. */
  path: string;
  /** The same path closed along the horizon: the part above it is the day's sunlight. */
  area: string;
  /** The horizon's y. */
  horizon: number;
  /** Highest point, with its elevation in degrees, if the sun comes up at all. */
  peak: { x: number; y: number; t: number; deg: number } | null;
}

export interface Strip {
  /** The sky's brightness and glow along the day; empty without a place. */
  sky: SkyStop[];
  /** Null without a place. */
  sun: SunTrack | null;
  bars: Bar[];
  /** Now, on today's strip only: x in px, left in percent, and the sun's y and elevation if it's known. */
  now: { x: number; left: number; y: number | null; deg: number | null } | null;
}

/** mm in an hour that fills the strip's height; heavier rain is clipped. */
export const FULL_BAR_MM = 2;
/** The tallest bar, as a share of the strip, so the sun stays readable above. */
const BAR_SHARE = 72;
/** Samples of the sun across a day: every 10 minutes. */
const SAMPLES = 144;
/** How far below the horizon the strip reaches, as sin(elevation): about 24° down, past astronomical twilight. */
const DEPTH = 0.4;

/**
 * The day's 24 (or 23, or 25) hours across a width×height strip: the sky,
 * the sun's track and rain bars. The track's height is sin(elevation): 0 at
 * the horizon, the top of the strip with the sun overhead. That is also how
 * strongly the sun lights level ground, so the area between the track and
 * the horizon is the day's sunlight, before cloud.
 */
export function strip(d: Day, nowSec: number, width: number, height: number, place: Place | null): Strip {
  const start = Number(d.start);
  const span = Number(d.end) - start;
  const frac = (t: number) => (t - start) / span;

  const top = 4;
  const horizon = top + (height - top) / (1 + DEPTH);
  const y = (deg: number) => horizon - Math.sin((deg * Math.PI) / 180) * (horizon - top);
  const elevation = (t: number) => (place ? solarElevation(place.lat, place.lon, t) : 0);

  const sky: SkyStop[] = [];
  let sun: SunTrack | null = null;
  if (place) {
    const pts: string[] = [];
    let peak: SunTrack["peak"] = null;
    for (let k = 0; k <= SAMPLES; k++) {
      const t = start + (span * k) / SAMPLES;
      const deg = elevation(t);
      sky.push({ offset: k / SAMPLES, light: skyLight(deg), glow: horizonGlow(deg) });
      const x = (k / SAMPLES) * width;
      pts.push(`${x.toFixed(1)} ${y(deg).toFixed(1)}`);
      if (deg > 0 && (!peak || deg > peak.deg)) peak = { x, y: y(deg), t, deg };
    }
    const path = "M" + pts.join(" L");
    sun = {
      path,
      area: `${path} L${width} ${horizon.toFixed(1)} L0 ${horizon.toFixed(1)} Z`,
      horizon,
      peak: peak && { ...peak, t: Math.round(peak.t) },
    };
  }

  const bars: Bar[] = [];
  const hourW = (3600 / span) * 100;
  d.hourlyPrecipMm.forEach((mm, i) => {
    if (mm < 0.05) return;
    const chance = d.hourlyPrecipChance[i] ?? 0;
    bars.push({
      left: i * hourW + hourW * 0.12,
      width: hourW * 0.76,
      height: Math.min(mm / FULL_BAR_MM, 1) * BAR_SHARE,
      opacity: 0.25 + (0.75 * Math.min(chance, 100)) / 100,
    });
  });

  const isToday = start <= nowSec && nowSec < Number(d.end);
  const deg = place ? elevation(nowSec) : null;
  const now = isToday
    ? { x: frac(nowSec) * width, left: frac(nowSec) * 100, y: deg == null ? null : y(deg), deg }
    : null;

  return { sky, sun, bars, now };
}

/** Percent along the strip, for a label under it. */
export const along = (d: Day, t: number): number => ((t - Number(d.start)) / (Number(d.end) - Number(d.start))) * 100;
