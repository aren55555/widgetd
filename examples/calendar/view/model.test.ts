import { create } from "@bufbuild/protobuf";
import { describe, expect, test } from "bun:test";

import { EventSchema, StateSchema, WorkingHoursSchema } from "./gen/calendar/v1/calendar_pb.ts";
import {
  clockLabel,
  fortnight,
  hourLabels,
  instant,
  lanes,
  overlaps,
  segments,
  unpack,
  wall,
  weekday,
  type Prefs,
  type Span,
} from "./model.ts";

const LA: Prefs = { locale: "en-US", timeZone: "America/Los_Angeles", hour12: true };
const TOKYO: Prefs = { locale: "ja-JP", timeZone: "Asia/Tokyo", hour12: false };
const civil = (y: number, m: number, d: number) => Date.UTC(y, m - 1, d) / 86_400_000;

describe("zones", () => {
  test("wall and instant agree", () => {
    const ms = Date.UTC(2026, 10, 10, 17, 0); // 09:00 in Los Angeles
    expect(wall(ms, LA.timeZone)).toEqual({ day: civil(2026, 11, 10), min: 9 * 60 });
    expect(instant(civil(2026, 11, 10), 9 * 60, LA.timeZone)).toBe(ms);
  });

  test("DST days are 23 and 25 hours", () => {
    const len = (y: number, m: number, d: number) =>
      (instant(civil(y, m, d) + 1, 0, LA.timeZone) - instant(civil(y, m, d), 0, LA.timeZone)) / 60_000;
    expect(len(2026, 3, 8)).toBe(23 * 60);
    expect(len(2026, 11, 1)).toBe(25 * 60);
    expect(len(2026, 11, 2)).toBe(24 * 60);
  });

  test("weekday", () => {
    expect(weekday(0)).toBe(4); // 1970-01-01 was a Thursday
    expect(weekday(civil(2026, 11, 8))).toBe(0);
  });
});

test("unpack", () => {
  expect(unpack([10, 6, 3, 9, 0, 1])).toEqual([
    [10, 16],
    [13, 22],
    [13, 14],
  ]);
});

// A state whose window starts 2026-11-02 00:00 UTC, as the server would
// send it on 2026-11-10.
const WS = Date.UTC(2026, 10, 2) / 1000;
const units = (iso: string) => (Date.parse(iso) / 1000 - WS) / 300;
function state(busy: Array<[string, string, string]>, tentative: Array<[string, string, string]> = []) {
  const pack = (xs: Array<[string, string, string]>) => {
    let prev = 0;
    return xs.flatMap(([a, b]) => {
      const s = units(a);
      const out = [s - prev, units(b) - s];
      prev = s;
      return out;
    });
  };
  return create(StateSchema, {
    windowStart: BigInt(WS),
    resolutionMinutes: 5,
    workingHours: create(WorkingHoursSchema, { start: 540, end: 1050, days: 0b0111110 }),
    busy: pack(busy),
    tentative: pack(tentative),
    events: [...busy, ...tentative].map(([, , title]) => create(EventSchema, { title })),
    allDay: [{ firstDay: civil(2026, 11, 16), days: 2, title: "Offsite" }],
  });
}

describe("fortnight", () => {
  const now = Date.UTC(2026, 10, 10, 20, 0); // Tue 12:00 in Los Angeles; Wed 05:00 in Tokyo
  const st = state(
    [
      ["2026-11-10T17:00:00Z", "2026-11-10T17:30:00Z", "Standup"],
      ["2026-11-14T06:00:00Z", "2026-11-14T10:00:00Z", "Red-eye"], // Fri 22:00 → Sat 02:00 in LA
    ],
    [["2026-11-11T18:00:00Z", "2026-11-11T19:00:00Z", "Design review"]],
  );

  test("starts on this week's Sunday, in the screen's zone", () => {
    const la = fortnight(st, now, LA);
    const tokyo = fortnight(st, now, TOKYO);
    expect(la).toHaveLength(14);
    expect(la[0].day).toBe(civil(2026, 11, 8));
    expect(tokyo[0].day).toBe(civil(2026, 11, 8));
    expect(la[2].today).toBe(true); // Tuesday
    expect(tokyo[3].today).toBe(true); // already Wednesday
    expect(la[2].now).toBe(12 * 60);
    expect(la[1].pastUntil).toBe(la[1].len);
    expect(la[3].pastUntil).toBe(0);
  });

  test("events land on the screen's local days", () => {
    const la = fortnight(st, now, LA);
    expect(la[2].spans.map((x) => [x.s, x.e, x.ev?.title])).toEqual([[9 * 60, 9 * 60 + 30, "Standup"]]);
    expect(la[3].spans.map((x) => [x.s, x.k])).toEqual([[10 * 60, true]]);
    // In Tokyo the same standup is Wednesday 02:00.
    const tokyo = fortnight(st, now, TOKYO);
    expect(tokyo[3].spans[0].s).toBe(2 * 60);
  });

  test("an event across midnight is cut in two", () => {
    const la = fortnight(st, now, LA);
    expect(la[5].spans.map((x) => [x.s, x.e])).toEqual([[22 * 60, 24 * 60]]);
    expect(la[6].spans.map((x) => [x.s, x.e])).toEqual([[0, 2 * 60]]);
    // Both halves know the whole event, for its label.
    expect(la[6].spans[0].from).toBe(Date.parse("2026-11-14T06:00:00Z"));
  });

  test("all-day events, working days, weekends", () => {
    const la = fortnight(st, now, LA);
    expect(la[8].allDay).toEqual(["Offsite"]);
    expect(la[9].allDay).toEqual(["Offsite"]);
    expect(la[10].allDay).toEqual([]);
    expect(la.map((d) => d.working)).toEqual([
      false,
      true,
      true,
      true,
      true,
      true,
      false,
      false,
      true,
      true,
      true,
      true,
      true,
      false,
    ]);
    expect(la[0].weekend && la[6].weekend && !la[1].weekend).toBe(true);
    expect(la[2].work).toEqual([540, 1050]);
  });

  test("working hours stay on the wall clock across a DST change", () => {
    const march = fortnight(state([]), Date.UTC(2026, 2, 9, 20, 0), LA);
    const dst = march[0]; // Sunday 2026-03-08: clocks jump at 02:00
    expect(dst.len).toBe(23 * 60);
    const mon = march[1];
    expect(mon.work).toEqual([540, 1050]);
  });
});

describe("layout", () => {
  const sp = (s: number, e: number, k = false): Span => ({ s, e, k, from: 0, to: 0 });

  test("segments colour double-booked and tentative time", () => {
    expect(segments([sp(0, 60), sp(30, 90), sp(80, 120, true)])).toEqual([
      { s: 0, e: 30, cat: "busy" },
      { s: 30, e: 60, cat: "double" },
      { s: 60, e: 80, cat: "busy" },
      { s: 80, e: 90, cat: "busy" },
      { s: 90, e: 120, cat: "tent" },
    ]);
  });

  test("lanes and overlaps", () => {
    const xs = [sp(0, 60), sp(30, 90), sp(60, 120), sp(70, 80, true)];
    expect(lanes(xs)).toEqual({ lane: [0, 1, 0, 2], count: 3 });
    expect(overlaps(xs, 1).length).toBe(2);
    expect(overlaps(xs, 3)).toEqual([]); // tentative is never double-booked
  });

  test("working-hour labels sit at the Mon–Fri block's outer corners, weekend side", () => {
    const days = fortnight(state([]), Date.UTC(2026, 10, 10, 20, 0), LA);
    const labels = days.map((_, i) => hourLabels(days, i, 7));
    expect(labels).toEqual([["right"], [], [], [], [], [], ["left"], ["right"], [], [], [], [], [], ["left"]]);
  });

  test("clock labels follow the display's hour cycle", () => {
    expect(clockLabel(9 * 60, true)).toBe("9a");
    expect(clockLabel(17 * 60 + 30, true)).toBe("5:30p");
    expect(clockLabel(9 * 60, false)).toBe("09");
    expect(clockLabel(17 * 60 + 30, false)).toBe("17:30");
    expect(clockLabel(0, true)).toBe("12a");
  });
});
