// How a game card reads, apart from the DOM so it can be tested. Times are
// shown in the viewer's own zone.

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
