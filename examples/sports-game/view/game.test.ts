import { describe, expect, test } from "bun:test";
import { startLabel, teamColor } from "./game.ts";

const tz = "America/Los_Angeles";
const at = (iso: string) => Date.parse(iso);

describe("startLabel", () => {
  const now = at("2026-11-08T18:00:00Z"); // Sun 10:00 AM in LA
  test("today, by time of day", () => {
    expect(startLabel(at("2026-11-08T21:05:00Z"), now, tz)).toBe("Today 1:05 PM");
    expect(startLabel(at("2026-11-09T03:00:00Z"), now, tz)).toBe("Tonight 7:00 PM");
  });
  test("tomorrow, this week, later", () => {
    expect(startLabel(at("2026-11-09T21:00:00Z"), now, tz)).toBe("Tomorrow 1:00 PM");
    expect(startLabel(at("2026-11-12T03:30:00Z"), now, tz)).toBe("Wed 7:30 PM");
    expect(startLabel(at("2026-11-30T21:00:00Z"), now, tz)).toBe("Nov 30, 1:00 PM");
  });
});

test("teamColor", () => {
  expect(teamColor("aa0000")).toBe("#aa0000");
  expect(teamColor("")).toBeUndefined();
  expect(teamColor("red")).toBeUndefined();
});
