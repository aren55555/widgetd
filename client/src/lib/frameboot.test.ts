import { expect, test } from "bun:test";
import type { FromFrame } from "@widgetd/sdk/bridge";
import { reportUncaught, takeConsole } from "./frameboot.ts";

type Line = Extract<FromFrame, { t: "log" }>;

/** A console shaped like a browser's: its methods are its own properties. */
function fakeConsole() {
  const printed: unknown[][] = [];
  const con: Record<string, unknown> = {};
  for (const m of ["log", "info", "debug", "warn", "error", "trace", "table", "assert", "group", "time", "count"]) {
    con[m] = (...args: unknown[]) => printed.push([m, ...args]);
  }
  return { con, printed };
}

test("console calls become log lines at their level, and print nothing", () => {
  const { con, printed } = fakeConsole();
  const sent: Line[] = [];
  const global = { console: con };
  takeConsole(con, global, (m) => sent.push(m));
  const c = global.console as unknown as Console;
  c.log("hi", 3, { a: 1 });
  c.warn("careful");
  c.error("failed:", new Error("boom"));
  c.debug("d");
  c.trace("t");
  c.group("g");
  c.time("t");
  c.assert(true, "fine");
  c.assert(false, "broken", 2);
  expect(printed).toEqual([]);
  expect(sent.map((l) => [l.level, l.message, l.attrs.console])).toEqual([
    ["info", 'hi 3 {"a":1}', "log"],
    ["warn", "careful", "warn"],
    ["error", "failed: boom", "error"],
    ["debug", "d", "debug"],
    ["debug", "t", "trace"],
    ["error", "Assertion failed: broken 2", "assert"],
  ]);
  expect(sent[2]?.attrs.stack).toContain("boom");
});

test("a view can't put a console back", () => {
  const { con, printed } = fakeConsole();
  const global = { console: con };
  const original = con.log;
  takeConsole(con, global, () => undefined);
  // In strict code each attempt throws; in sloppy code it fails silently.
  expect(() => {
    con.log = original;
  }).toThrow();
  expect(() => Object.defineProperty(con, "log", { value: original })).toThrow();
  expect(() => {
    (global as { console: unknown }).console = { log: original };
  }).toThrow();
  expect(() => Object.defineProperty(global, "console", { value: {} })).toThrow();
  expect(() => {
    con.extra = original;
  }).toThrow();
  (global.console as unknown as Console).log("still ours");
  expect(printed).toEqual([]);
});

test("uncaught errors and rejections are sent", () => {
  const handlers: Partial<Record<string, (e: unknown) => void>> = {};
  const sent: Line[] = [];
  reportUncaught({ addEventListener: (t: string, h: (e: unknown) => void) => (handlers[t] = h) } as never, (m) =>
    sent.push(m),
  );
  handlers.error?.({ error: new Error("thrown") });
  handlers.error?.({ error: null, message: "Script error.", filename: "view.js", lineno: 7 });
  handlers.unhandledrejection?.({ reason: "nope" });
  expect(sent.map((l) => [l.level, l.message, l.attrs])).toEqual([
    ["error", "thrown", { uncaught: "true", stack: expect.any(String) as string }],
    ["error", "Script error.", { uncaught: "true", at: "view.js:7" }],
    ["error", "nope", { uncaught: "true", rejection: "true" }],
  ]);
});

test("methods an engine keeps on a prototype are taken too", () => {
  const printed: unknown[] = [];
  const proto = { log: (...a: unknown[]) => printed.push(a), context: () => ({ log: () => printed.push("fresh") }) };
  const con = Object.create(proto) as Record<string, unknown>;
  const sent: unknown[] = [];
  takeConsole(con, { console: con }, (m) => sent.push(m));
  (con as unknown as Console).log("x");
  expect((con.context as () => unknown)()).toBeUndefined();
  expect(printed).toEqual([]);
  expect(sent.length).toBe(1);
});
