// Display preferences: how a screen shows times and quantities. The board
// resolves them once, from what its URL sets and what the display reports,
// and hands the result to every card as `ctx.prefs`. A card on an older
// board, which sends none, resolves them here itself, the same way.
//
// Detection follows the ICU/CLDR convention that Intl, iOS and Android use:
// the clock comes from the locale, and units from the locale's region.
// Pure: everything about the display comes in through `env`.

import { create } from "@bufbuild/protobuf";
import {
  DistanceUnit,
  HourCycle,
  MassUnit,
  PreferencesSchema,
  SpeedUnit,
  TemperatureUnit,
  type Preferences,
} from "./gen/widgetd/v1/prefs_pb.ts";

export { DistanceUnit, HourCycle, MassUnit, PreferencesSchema, SpeedUnit, TemperatureUnit, type Preferences };

type Fields = Omit<Preferences, "$typeName" | "$unknown">;

/**
 * Preferences with every field filled in. Enums never hold their
 * `*_UNSPECIFIED` value, so an exhaustive `switch` over a unit breaks at
 * compile time when a unit is added.
 */
export type DisplayPrefs = { readonly [K in keyof Fields]-?: Exclude<NonNullable<Fields[K]>, 0> };

/** Where a resolved value came from. */
export type Source =
  | { kind: "set" }
  /** `from` is the evidence: "browser", or the region units were taken from. */
  | { kind: "detected"; from: string }
  | { kind: "default" };

export interface ResolvedPrefs {
  prefs: DisplayPrefs;
  sources: { readonly [K in keyof DisplayPrefs]: Source };
}

/** What the display reports about itself. */
export interface PrefsEnv {
  language?: string;
  timeZone?: string;
}

export const DEFAULT_LOCALE = "en-US";
export const DEFAULT_TIME_ZONE = "UTC";

/** The display's own language and zone, as the browser reports them. */
export function browserEnv(): PrefsEnv {
  let timeZone: string | undefined;
  try {
    timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  } catch {
    /* none */
  }
  return { language: typeof navigator === "undefined" ? undefined : navigator.language, timeZone };
}

const validLocale = (l: string | undefined): string | undefined => {
  if (!l) return undefined;
  try {
    return Intl.getCanonicalLocales(l)[0];
  } catch {
    return undefined;
  }
};

const validZone = (z: string | undefined): string | undefined => {
  if (!z) return undefined;
  try {
    return new Intl.DateTimeFormat("en", { timeZone: z }).resolvedOptions().timeZone;
  } catch {
    return undefined;
  }
};

// Regions whose CLDR unitPreferenceData differs from metric, per category
// (temperature: weather; speed and distance: road; mass: person). A small
// subset of the table: every region not listed is metric.
const FAHRENHEIT = new Set(["US", "BS", "BZ", "KY", "PR", "PW"]);
const ROAD_MILES = new Set(["US", "GB", "PR", "VI", "GU", "AS", "MP"]);
const POUNDS = new Set(["US", "LR", "MM"]);

type Units = Pick<DisplayPrefs, "temperature" | "speed" | "mass" | "distance">;

const metric: Units = {
  temperature: TemperatureUnit.CELSIUS,
  speed: SpeedUnit.KPH,
  mass: MassUnit.KG,
  distance: DistanceUnit.KM,
};

function unitsFor(region: string | undefined): Units {
  if (!region) return metric;
  return {
    temperature: FAHRENHEIT.has(region) ? TemperatureUnit.FAHRENHEIT : TemperatureUnit.CELSIUS,
    speed: ROAD_MILES.has(region) ? SpeedUnit.MPH : SpeedUnit.KPH,
    mass: POUNDS.has(region) ? MassUnit.LB : MassUnit.KG,
    distance: ROAD_MILES.has(region) ? DistanceUnit.MI : DistanceUnit.KM,
  };
}

/** A Unicode locale extension keyword's value, e.g. `ms` in `en-u-ms-metric`. */
function keyword(locale: string, key: string): string | undefined {
  const m = /-u-(.*)$/.exec(locale.toLowerCase());
  if (!m) return undefined;
  const parts = m[1].split("-");
  for (let i = 0; i < parts.length; i++) {
    if (parts[i].length === 1) break; // another extension starts
    if (parts[i] === key) return parts[i + 1];
  }
  return undefined;
}

/** The region units come from: `-u-rg-`, the locale's own, or its likely one. */
function regionOf(locale: string): string | undefined {
  const rg = keyword(locale, "rg");
  if (rg && /^[a-z]{2}/.test(rg)) return rg.slice(0, 2).toUpperCase();
  try {
    const l = new Intl.Locale(locale);
    return l.region ?? l.maximize().region;
  } catch {
    return undefined;
  }
}

/** `-u-ms-` and `-u-mu-` override what the region says. */
function overridden(units: Units, locale: string): { units: Units; by?: string } {
  let out = units;
  let by: string | undefined;
  switch (keyword(locale, "ms")) {
    case "metric":
      out = metric;
      by = "-u-ms-metric";
      break;
    case "ussystem":
      out = {
        temperature: TemperatureUnit.FAHRENHEIT,
        speed: SpeedUnit.MPH,
        mass: MassUnit.LB,
        distance: DistanceUnit.MI,
      };
      by = "-u-ms-ussystem";
      break;
    case "uksystem":
      out = {
        temperature: TemperatureUnit.CELSIUS,
        speed: SpeedUnit.MPH,
        mass: MassUnit.KG,
        distance: DistanceUnit.MI,
      };
      by = "-u-ms-uksystem";
      break;
    default:
    // None, or one we don't know: the region decides.
  }
  const mu = keyword(locale, "mu");
  if (mu === "celsius" || mu === "fahrenh") {
    out = { ...out, temperature: mu === "celsius" ? TemperatureUnit.CELSIUS : TemperatureUnit.FAHRENHEIT };
    by = by ? `${by}, -u-mu-${mu}` : `-u-mu-${mu}`;
  }
  return { units: out, by };
}

function hourCycleOf(locale: string): DisplayPrefs["hourCycle"] {
  try {
    const hc = new Intl.DateTimeFormat(locale, { hour: "numeric" }).resolvedOptions().hourCycle;
    return hc === "h23" || hc === "h24" ? HourCycle.H24 : HourCycle.H12;
  } catch {
    return HourCycle.H12;
  }
}

function regionName(region: string, locale: string): string {
  try {
    return new Intl.DisplayNames([locale], { type: "region" }).of(region) ?? region;
  } catch {
    return region;
  }
}

/**
 * Every preference filled in, each with where it came from. A field `partial`
 * sets (and that is valid) wins; otherwise it is detected from `env`, and
 * failing that it takes the default.
 */
export function resolvePrefs(partial: Preferences | undefined, env: PrefsEnv): ResolvedPrefs {
  const p = partial ?? create(PreferencesSchema);

  let locale: string;
  let localeSrc: Source;
  const setLocale = validLocale(p.locale);
  const envLocale = validLocale(env.language);
  if (setLocale) [locale, localeSrc] = [setLocale, { kind: "set" }];
  else if (envLocale) [locale, localeSrc] = [envLocale, { kind: "detected", from: "browser" }];
  else [locale, localeSrc] = [DEFAULT_LOCALE, { kind: "default" }];

  let timeZone: string;
  let zoneSrc: Source;
  const setZone = validZone(p.timeZone);
  const envZone = validZone(env.timeZone);
  if (setZone) [timeZone, zoneSrc] = [setZone, { kind: "set" }];
  else if (envZone) [timeZone, zoneSrc] = [envZone, { kind: "detected", from: "browser" }];
  else [timeZone, zoneSrc] = [DEFAULT_TIME_ZONE, { kind: "default" }];

  // What follows from the locale is as good as the locale: a default locale
  // gives default values.
  const fromLocale = (from: string): Source =>
    localeSrc.kind === "default" ? { kind: "default" } : { kind: "detected", from };

  const region = regionOf(locale);
  const { units, by } = overridden(unitsFor(region), locale);
  const unitSrc = fromLocale(by ?? (region ? regionName(region, locale) : "no region: metric"));

  // A value this build doesn't know (set by a newer schema) counts as unset.
  const known = (e: object, v: number) => v !== 0 && Object.values(e).includes(v);
  const pick = <K extends keyof Units>(k: K, e: object): [Units[K], Source] => {
    const v = p[k] as number;
    // known() just checked that v is one of the enum's values.
    // eslint-disable-next-line @typescript-eslint/no-unsafe-enum-assignment
    return known(e, v) ? [v as Units[K], { kind: "set" }] : [units[k], unitSrc];
  };
  const [temperature, temperatureSrc] = pick("temperature", TemperatureUnit);
  const [speed, speedSrc] = pick("speed", SpeedUnit);
  const [mass, massSrc] = pick("mass", MassUnit);
  const [distance, distanceSrc] = pick("distance", DistanceUnit);
  const [hourCycle, hourCycleSrc]: [DisplayPrefs["hourCycle"], Source] = known(HourCycle, p.hourCycle)
    ? [p.hourCycle as DisplayPrefs["hourCycle"], { kind: "set" }]
    : [hourCycleOf(locale), fromLocale(locale)];

  return {
    prefs: { locale, timeZone, hourCycle, temperature, speed, mass, distance },
    sources: {
      locale: localeSrc,
      timeZone: zoneSrc,
      hourCycle: hourCycleSrc,
      temperature: temperatureSrc,
      speed: speedSrc,
      mass: massSrc,
      distance: distanceSrc,
    },
  };
}

/** The resolved preferences as a message, to hand to a card. */
export const toMessage = (prefs: DisplayPrefs): Preferences => create(PreferencesSchema, { ...prefs });
