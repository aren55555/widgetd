// How a game card reads, apart from the DOM so it can be tested. Times are
// shown in the viewer's own zone.

import { League } from "./gen/sportsgame/v1/sportsgame_pb.ts";

const DAY = 86_400_000;

/** "Tonight 7:00 PM", "Tomorrow 1:05 PM", "Sat 4:25 PM", "Nov 30, 1:00 PM". */
export function startLabel(start: number, now: number, timeZone?: string): string {
  const fmt = (o: Intl.DateTimeFormatOptions) => new Intl.DateTimeFormat("en-US", { ...o, timeZone }).format(start);
  const time = fmt({ hour: "numeric", minute: "2-digit" });
  const dayKey = (t: number) =>
    new Intl.DateTimeFormat("en-CA", { timeZone, year: "numeric", month: "2-digit", day: "2-digit" }).format(t);
  const days = Math.round((Date.parse(dayKey(start)) - Date.parse(dayKey(now))) / DAY);
  if (days === 0) return `${Number(fmt({ hour: "numeric", hour12: false })) >= 17 ? "Tonight" : "Today"} ${time}`;
  if (days === 1) return `Tomorrow ${time}`;
  if (days > 1 && days < 7) return `${fmt({ weekday: "short" })} ${time}`;
  return `${fmt({ month: "short", day: "numeric" })}, ${time}`;
}

/** A team colour usable as an accent, or undefined if ESPN gave none. */
export const teamColor = (hex: string) => (/^[0-9a-f]{6}$/i.test(hex) ? `#${hex}` : undefined);

/** How long a game usually runs, as the chart's time axis before it runs over. Matches the server's. */
export const TYPICAL_MS: Partial<Record<League, number>> = {
  [League.NFL]: 4 * 3_600_000,
  [League.NHL]: 3 * 3_600_000,
  [League.NBA]: 3 * 3_600_000,
  [League.MLB]: 4 * 3_600_000,
  [League.WNBA]: 3 * 3_600_000,
  [League.MLS]: 2 * 3_600_000,
};

/**
 * Whether two hex colours are too alike to tell two lines apart: a
 * "redmean" distance, which weighs channels roughly as the eye does.
 */
export function tooAlike(a: string, b: string): boolean {
  const rgb = (h: string) => [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16));
  const [x, y] = [rgb(a), rgb(b)];
  const r = (x[0] + y[0]) / 2;
  const [dr, dg, db] = x.map((v, i) => v - y[i]) as [number, number, number];
  return Math.sqrt((2 + r / 256) * dr ** 2 + 4 * dg ** 2 + (2 + (255 - r) / 256) * db ** 2) < 120;
}

/** A team colour lifted toward white if it's too dark to read on the dark card (navy, black). */
export function readable(hex: string): string {
  const n = parseInt(hex, 16);
  const luma = 0.2126 * (n >> 16) + 0.7152 * ((n >> 8) & 255) + 0.0722 * (n & 255);
  return luma < 60 ? `color-mix(in oklab, #${hex} 65%, white)` : `#${hex}`;
}

interface Colors {
  color: string;
  alternateColor: string;
}

/**
 * Each team's line colour: its primary, except that the away team takes its
 * alternate when the two primaries are too alike. A team with no colour
 * gets the theme's ink.
 */
export function lineColors(away: Colors, home: Colors): { away: string; home: string } {
  const ok = (h: string) => /^[0-9a-f]{6}$/i.test(h);
  let a = away.color;
  if (
    ok(a) &&
    ok(home.color) &&
    tooAlike(a, home.color) &&
    ok(away.alternateColor) &&
    !tooAlike(away.alternateColor, home.color)
  ) {
    a = away.alternateColor;
  }
  const css = (h: string) => (ok(h) ? readable(h) : "var(--wd-ink-muted, #a1a1aa)");
  return { away: css(a), home: css(home.color) };
}

/**
 * How far above and below 50% the chart reaches: the furthest price from
 * 50 so far, plus a tenth, between 8 and 50. Prices a decided game ends on
 * (≥97, ≤3) don't count, so the final jump runs off the card rather than
 * flattening the game. Symmetric, so 50% stays at the centre.
 */
export function halfRange(...series: readonly number[][]): number {
  let far = 0;
  for (const s of series) for (const p of s) if (p > 3 && p < 97) far = Math.max(far, Math.abs(p - 50));
  return Math.min(50, Math.max(8, far * 1.1));
}

/**
 * Tags at ys (px), moved apart to at least gap px if two are closer, keeping
 * their midpoint. For the hover's two price tags.
 */
export function spread(ys: readonly number[], gap: number): number[] {
  const out = [...ys];
  if (out.length === 2 && Math.abs(out[0] - out[1]) < gap) {
    const mid = (out[0] + out[1]) / 2;
    const upper = out[0] <= out[1] ? 0 : 1;
    out[upper] = mid - gap / 2;
    out[1 - upper] = mid + gap / 2;
  }
  return out;
}
