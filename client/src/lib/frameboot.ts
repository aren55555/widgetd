// What runs in a card frame before the widget's own code: the host inlines
// it (frame.ts, built to dist/frame.js) ahead of view.js. It takes over the
// frame's console, so every console call goes to the server's log through
// the board, and reports what the view throws and doesn't catch. Nothing
// a view does can come before it, so a view can't keep a console of its
// own: the methods are replaced, then locked in place.

import type { FromFrame, LogLevel } from "@widgetd/sdk/bridge";
import { consoleLine, logLine } from "@widgetd/sdk/log";

/** Console methods that print, and the level each prints at. */
export const CONSOLE_LEVELS: Readonly<Partial<Record<string, LogLevel>>> = {
  debug: "debug",
  trace: "debug",
  log: "info",
  info: "info",
  dir: "info",
  dirxml: "info",
  table: "info",
  warn: "warn",
  error: "error",
};

type Send = (m: Extract<FromFrame, { t: "log" }>) => void;

/**
 * Replace every method of `con` with one that sends its line instead of
 * printing it, then lock them, and `con` itself on `global`, so they can't
 * be swapped back. Methods that don't print (group, time, count…) do
 * nothing, so none reaches the frame's own console either.
 */
export function takeConsole(con: object, global: object, send: Send): void {
  const methods: Record<string, (...args: unknown[]) => void> = {};
  // Own methods, and any an engine keeps on a prototype instead, each
  // shadowed by an own property, so none is reachable through `con`.
  // (console.context() made a fresh console; it now does nothing.)
  const names = new Set<string>();
  for (let o: object | null = con; o && o !== Object.prototype; o = Object.getPrototypeOf(o) as object | null) {
    for (const name of Object.getOwnPropertyNames(o)) names.add(name);
  }
  names.delete("constructor");
  for (const name of names) {
    if (typeof (con as Record<string, unknown>)[name] !== "function") continue;
    const level = CONSOLE_LEVELS[name];
    methods[name] = level
      ? (...args) => {
          send(consoleLine(level, name, args));
        }
      : () => undefined;
  }
  methods.assert = (cond?: unknown, ...args: unknown[]) => {
    if (!cond) send(consoleLine("error", "assert", ["Assertion failed:", ...args]));
  };
  for (const [name, fn] of Object.entries(methods)) {
    Object.defineProperty(con, name, { value: fn, writable: false, configurable: false, enumerable: true });
  }
  Object.freeze(con);
  Object.defineProperty(global, "console", { value: con, writable: false, configurable: false });
}

/** Send what the view throws and doesn't catch, from the first line of its code. */
export function reportUncaught(target: Pick<Window, "addEventListener">, send: Send): void {
  target.addEventListener("error", (e: ErrorEvent) => {
    const err: unknown = e.error;
    send(
      logLine("error", err ?? e.message, {
        uncaught: true,
        ...(err ? {} : { at: `${e.filename}:${String(e.lineno)}` }),
      }),
    );
  });
  target.addEventListener("unhandledrejection", (e: PromiseRejectionEvent) => {
    send(logLine("error", e.reason, { uncaught: true, rejection: true }));
  });
}
