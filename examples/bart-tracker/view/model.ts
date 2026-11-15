// The tracker's pure logic: where a train is, what to count down, and how
// times and names read. No DOM, so it's tested directly.

import { HourCycle, type DisplayPrefs } from "@widgetd/sdk";

import type { Departure, Train } from "./gen/barttracker/v1/barttracker_pb.ts";

/** How many departures each column shows. */
export const SHOWN = 3;
/** A train stays listed this long after it leaves, as "0". */
export const LEFT_GRACE_MS = 30_000;

/**
 * Where a train is now, in stations along the line (0 is the first): between
 * from_index and to_index by the share of that stretch's scheduled time that
 * has passed, or at to_index once it's due there.
 */
export function trainPosition(t: Train, nowMs: number): number {
  const from = t.fromIndex;
  const to = t.toIndex;
  if (from === to || t.stretchSeconds === 0) return to;
  const left = (Number(t.arriveUnixMs) - nowMs) / 1000;
  const f = Math.min(1, Math.max(0, 1 - left / t.stretchSeconds));
  return from + (to - from) * f;
}

/** Standing at its station (rather than between two). */
export const standing = (t: Train, nowMs: number): boolean =>
  t.fromIndex === t.toIndex || Number(t.arriveUnixMs) <= nowMs;

/** Whole minutes from now until ms, never below zero. */
export const minutesUntil = (ms: bigint | number, nowMs: number): number =>
  Math.max(0, Math.round((Number(ms) - nowMs) / 60_000));

/** The departures a column shows: not long gone, soonest first, at most SHOWN. */
export const upcoming = (deps: readonly Departure[], nowMs: number): Departure[] =>
  deps.filter((d) => Number(d.leavesUnixMs) > nowMs - LEFT_GRACE_MS).slice(0, SHOWN);

/** "Antioch · next Orinda in 4 min", or "… · at Orinda" while it's there. */
export function trainLabel(t: Train, next: string, nowMs: number): string {
  return standing(t, nowMs)
    ? `${shortName(t.destination)} · at ${shortName(next)}`
    : `${shortName(t.destination)} · next ${shortName(next)} in ${minutesUntil(t.arriveUnixMs, nowMs)} min`;
}

// BART's names, shortened for the map and the board. Anything not listed
// keeps its name, with "Street" as "St".
const SHORT: Record<string, string> = {
  "Montgomery Street": "Montgomery",
  "12th Street / Oakland City Center": "12th St",
  "19th Street Oakland": "19th St",
  "West Oakland": "W Oakland",
  "Civic Center / UN Plaza": "Civic Center",
  "24th Street / Mission": "24th St / Mission",
  "16th Street / Mission": "16th St / Mission",
  "San Francisco International Airport": "SFO",
  "San Francisco Int'l Airport": "SFO",
  "Oakland International Airport": "OAK",
  "Pleasant Hill / Contra Costa Centre": "Pleasant Hill",
  "Dublin / Pleasanton": "Dublin",
  "Berryessa / North San Jose": "Berryessa",
  "Warm Springs / South Fremont": "Warm Springs",
  "Coliseum / Oakland Airport": "Coliseum",
  "North Concord / Martinez": "N Concord",
  "El Cerrito del Norte": "El Cerrito Norte",
};
export const shortName = (name: string): string => SHORT[name] ?? name.replace(/\bStreet\b/g, "St");

/** Dark ink on light line colours (yellow), white on dark ones (red, blue). */
export function inkFor(hex: string): string {
  const n = parseInt(hex.replace(/^#/, ""), 16);
  const l = 0.299 * (n >> 16) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255);
  return l > 150 ? "#18181b" : "#ffffff";
}

/** The characters a split-flap tile turns through, blank first. */
export const DRUM = " 0123456789";

/**
 * The characters a tile shows on its way from one to another, ending at the
 * new one: a drum only turns forward, so 2 to 8 passes 3 to 7, and 8 to 2
 * passes 9, blank, 0 and 1. A character not on the drum is reached at once.
 */
export function drumSteps(from: string, to: string): string[] {
  const a = DRUM.indexOf(from);
  const b = DRUM.indexOf(to);
  if (from === to) return [];
  if (a < 0 || b < 0) return [to];
  const out: string[] = [];
  for (let k = (a + 1) % DRUM.length; ; k = (k + 1) % DRUM.length) {
    out.push(DRUM.charAt(k));
    if (k === b) return out;
  }
}

/** A clock time on this screen: "11:41" for the board, and "11:41 AM" in full. */
export function clock(ms: bigint | number, prefs: DisplayPrefs): { short: string; full: string } {
  const full = new Intl.DateTimeFormat(prefs.locale, {
    hour: "numeric",
    minute: "2-digit",
    timeZone: prefs.timeZone,
    hourCycle: prefs.hourCycle === HourCycle.H24 ? "h23" : "h12",
  }).format(new Date(Number(ms)));
  // The board has no room for a day period; the countdown says which hour.
  const short = full.replace(/\s*[^\d\s:.]+\.?$/u, "").replace(/^[^\d]+/u, "");
  return { short, full };
}
