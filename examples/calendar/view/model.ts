// The calendar view's model: everything that isn't drawing. The server
// sends one UTC timeline; this cuts it into the 14 days a screen shows, in
// that screen's time zone. Pure, so it's tested without a browser.

import type { Event, State } from "./gen/calendar/v1/calendar_pb.ts";

/** What the display is set to. Board options will hand these in as ctx.prefs; until then, detectPrefs. */
export interface Prefs {
  locale: string;
  timeZone: string;
  hour12: boolean;
}

export function detectPrefs(): Prefs {
  const o = new Intl.DateTimeFormat(undefined, { hour: "numeric" }).resolvedOptions();
  return {
    locale: o.locale || "en-US",
    timeZone: o.timeZone || "UTC",
    hour12: o.hourCycle ? o.hourCycle === "h11" || o.hourCycle === "h12" : !!o.hour12,
  };
}

// ---------- time zones ----------

const MIN = 60_000;
const DAY = 86_400_000;

const parters = new Map<string, Intl.DateTimeFormat>();
function parter(tz: string): Intl.DateTimeFormat {
  let f = parters.get(tz);
  if (!f) {
    f = new Intl.DateTimeFormat("en-US", {
      timeZone: tz,
      hourCycle: "h23",
      year: "numeric",
      month: "numeric",
      day: "numeric",
      hour: "numeric",
      minute: "numeric",
    });
    parters.set(tz, f);
  }
  return f;
}

/** The wall clock in tz at ms: a civil day number (days since 1970-01-01) and minutes into it. */
export function wall(ms: number, tz: string): { day: number; min: number } {
  const p: Record<string, number> = {};
  for (const x of parter(tz).formatToParts(ms)) if (x.type !== "literal") p[x.type] = Number(x.value);
  const local = Date.UTC(p.year, p.month - 1, p.day, p.hour % 24, p.minute);
  return { day: Math.floor(local / DAY), min: Math.round((local - Math.floor(local / DAY) * DAY) / MIN) };
}

/** How far tz is ahead of UTC at ms, in minutes. */
function offset(ms: number, tz: string): number {
  const w = wall(ms, tz);
  return Math.round((w.day * DAY + w.min * MIN - Math.floor(ms / MIN) * MIN) / MIN);
}

/**
 * The instant tz's clocks read `min` minutes into civil day `day`. A time
 * skipped by a DST jump lands just after the jump.
 */
export function instant(day: number, min: number, tz: string): number {
  const guess = day * DAY + min * MIN;
  const a = guess - offset(guess, tz) * MIN;
  return guess - offset(a, tz) * MIN;
}

/** Day of the week of a civil day: 0 Sunday … 6 Saturday. */
export const weekday = (day: number) => (((day + 4) % 7) + 7) % 7;

// ---------- days ----------

export interface Span {
  /** Minutes from the day's start (real minutes: a DST day is 23 or 25 h). */
  s: number;
  e: number;
  /** Tentative: not yet accepted, or marked free. */
  k: boolean;
  /** Its event's details, if the card carries them. */
  ev?: Event;
  /** The whole event's instants, for its labels. */
  from: number;
  to: number;
}

export interface Day {
  day: number; // civil day number
  start: number; // ms
  len: number; // minutes
  spans: Span[];
  allDay: string[]; // titles ("" at the busy-only level)
  working: boolean;
  /** Working hours, in minutes from the day's start. */
  work: [number, number];
  today: boolean;
  weekend: boolean;
  /** Dim [0, pastUntil). */
  pastUntil: number;
  now: number | null;
}

/** Expands packed [start − previous start, duration] pairs into [start, end] unit offsets. */
export function unpack(packed: readonly number[]): Array<[number, number]> {
  const out: Array<[number, number]> = [];
  let at = 0;
  for (let i = 0; i + 1 < packed.length; i += 2) {
    at += packed[i];
    out.push([at, at + packed[i + 1]]);
  }
  return out;
}

/** The 14 days from this week's Sunday, as the screen in prefs.timeZone sees them. */
export function fortnight(st: State, now: number, prefs: Prefs): Day[] {
  const tz = prefs.timeZone;
  const ws = Number(st.windowStart) * 1000;
  const unit = (st.resolutionMinutes || 5) * MIN;
  const busy = unpack(st.busy);
  const tent = unpack(st.tentative);
  const all = [
    ...busy.map(([a, b], i) => ({ from: ws + a * unit, to: ws + b * unit, k: false, ev: st.events[i] })),
    ...tent.map(([a, b], i) => ({ from: ws + a * unit, to: ws + b * unit, k: true, ev: st.events[busy.length + i] })),
  ];
  const wh = st.workingHours ?? { start: 540, end: 1050, days: 0b0111110 };
  const today = wall(now, tz).day;
  const sunday = today - weekday(today);
  const days: Day[] = [];
  for (let i = 0; i < 14; i++) {
    const day = sunday + i;
    const start = instant(day, 0, tz);
    const end = instant(day + 1, 0, tz);
    const len = Math.round((end - start) / MIN);
    const at = (ms: number) => Math.round((ms - start) / MIN);
    const spans: Span[] = [];
    for (const x of all) {
      if (x.to <= start || x.from >= end) continue;
      spans.push({ s: Math.max(0, at(x.from)), e: Math.min(len, at(x.to)), k: x.k, ev: x.ev, from: x.from, to: x.to });
    }
    spans.sort((a, b) => a.s - b.s || a.e - b.e);
    const allDay = st.allDay.filter((a) => a.firstDay <= day && day < a.firstDay + a.days).map((a) => a.title);
    const wd = weekday(day);
    const nowMin = day === today ? at(now) : null;
    days.push({
      day,
      start,
      len,
      spans,
      allDay,
      working: ((wh.days >> wd) & 1) === 1,
      work: [at(instant(day, wh.start, tz)), at(instant(day, Math.min(wh.end, 1440), tz))],
      today: day === today,
      weekend: wd === 0 || wd === 6,
      pastUntil: day < today ? len : (nowMin ?? 0),
      now: nowMin,
    });
  }
  return days;
}

// ---------- layout ----------

export type Cat = "busy" | "double" | "tent";

/**
 * Splits a day at every event edge; each piece is busy (one accepted
 * event, with or without tentative ones), double (two or more accepted) or
 * tent (tentative only).
 */
export function segments(spans: readonly Span[]): Array<{ s: number; e: number; cat: Cat }> {
  const cuts = [...new Set(spans.flatMap((x) => [x.s, x.e]))].sort((a, b) => a - b);
  const out: Array<{ s: number; e: number; cat: Cat }> = [];
  for (let i = 0; i + 1 < cuts.length; i++) {
    const a = cuts[i];
    const b = cuts[i + 1];
    let busy = 0;
    let tent = 0;
    for (const x of spans) {
      if (x.s > a || x.e < b) continue;
      if (x.k) tent++;
      else busy++;
    }
    const cat = busy >= 2 ? "double" : busy ? "busy" : tent ? "tent" : null;
    if (cat) out.push({ s: a, e: b, cat });
  }
  return out;
}

/** Lanes: each span takes the first lane free when it starts. */
export function lanes(spans: ReadonlyArray<{ s: number; e: number }>): { lane: number[]; count: number } {
  const ends: number[] = [];
  const lane: number[] = [];
  for (const x of spans) {
    let l = ends.findIndex((end) => end <= x.s);
    if (l < 0) {
      l = ends.length;
      ends.push(0);
    }
    ends[l] = x.e;
    lane.push(l);
  }
  return { lane, count: Math.max(1, ends.length) };
}

/** The accepted spans overlapping span j: it's double-booked with them. */
export function overlaps(spans: readonly Span[], j: number): Span[] {
  const x = spans[j];
  if (x.k) return [];
  return spans.filter((y, i) => i !== j && !y.k && y.s < x.e && x.s < y.e);
}

/**
 * Which edges of day i (in a grid `cols` wide) get the working-hour labels:
 * the outer corners of each run of working days, written on the weekend
 * side (in the neighbouring day off, or inside the working day when that
 * neighbour is on another row).
 */
export function hourLabels(days: readonly Day[], i: number, cols: number): Array<"left" | "right"> {
  const d = days[i];
  const out: Array<"left" | "right"> = [];
  const sameRow = (j: number) => j >= 0 && j < days.length && Math.floor(j / cols) === Math.floor(i / cols);
  const isW = (j: number) => sameRow(j) && days[j].working;
  if (d.working) {
    if (!isW(i - 1) && !sameRow(i - 1)) out.push("left");
    if (!isW(i + 1) && !sameRow(i + 1)) out.push("right");
  } else {
    if (isW(i + 1)) out.push("right");
    if (isW(i - 1)) out.push("left");
  }
  return out;
}

// ---------- words ----------

/** A wall-clock time, compactly: "9a", "5:30p" on a 12-hour clock; "09", "17:30" on a 24-hour one. */
export function clockLabel(min: number, hour12: boolean): string {
  const h = Math.floor(min / 60) % 24;
  const m = Math.round(min % 60);
  if (!hour12) return String(h).padStart(2, "0") + (m ? ":" + String(m).padStart(2, "0") : "");
  return String(h % 12 || 12) + (m ? ":" + String(m).padStart(2, "0") : "") + (h < 12 ? "a" : "p");
}

/** An instant's wall-clock label in the display's zone. */
export const timeLabel = (ms: number, p: Prefs) => clockLabel(wall(ms, p.timeZone).min, p.hour12);

/** "1h 30m", "45m". */
export function duration(min: number): string {
  if (min < 60) return `${min}m`;
  return `${Math.floor(min / 60)}h${min % 60 ? ` ${min % 60}m` : ""}`;
}

/** A civil day, formatted: the zone is irrelevant, so it's done in UTC. */
export function dayLabel(day: number, locale: string, opts: Intl.DateTimeFormatOptions): string {
  return new Date(day * DAY).toLocaleDateString(locale, { ...opts, timeZone: "UTC" });
}

/** The day of the month of a civil day. */
export const dateOf = (day: number) => new Date(day * DAY).getUTCDate();

/** When the screen next needs redrawing for the clock alone: the next whole minute. */
export const nextTick = (now: number) => MIN - (now % MIN) + 50;
