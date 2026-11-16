// A card's logger. A frame has no network, so each line goes to the board,
// which batches them to the host; the host writes them to its log under the
// card's instance and widget, tagged source=view. Nothing here touches the
// DOM, so it can be tested without a browser.

import type { FromFrame, LogLevel } from "./bridge.ts";

/** Anything worth attaching to a line. Values are sent as strings. */
export type LogAttrs = Record<string, unknown>;

export interface WidgetLogger {
  debug(message: string, attrs?: LogAttrs): void;
  info(message: string, attrs?: LogAttrs): void;
  warn(message: string, attrs?: LogAttrs): void;
  /** `error(e)` with an Error logs its message, with its stack as an attr. */
  error(message: string | Error, attrs?: LogAttrs): void;
}

/** A logger that hands every line to `send`. */
export function makeLogger(send: (m: Extract<FromFrame, { t: "log" }>) => void): WidgetLogger {
  const at = (level: LogLevel) => (message: unknown, attrs?: LogAttrs) => {
    send(logLine(level, message, attrs));
  };
  return { debug: at("debug"), info: at("info"), warn: at("warn"), error: at("error") };
}

/** One line as the bridge carries it. An Error message brings its stack along. */
export function logLine(level: LogLevel, message: unknown, attrs?: LogAttrs): Extract<FromFrame, { t: "log" }> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(attrs ?? {})) out[k] = attrString(v);
  let text: string;
  if (message instanceof Error) {
    text = message.message || message.name;
    if (message.stack && !("stack" in out)) out.stack = message.stack;
  } else {
    text = typeof message === "string" ? message : attrString(message);
  }
  return { t: "log", level, message: text, attrs: out };
}

/**
 * A console call as one line: its arguments joined with spaces, as the
 * console prints them, tagged with the method. The first Error's stack
 * comes along.
 */
export function consoleLine(
  level: LogLevel,
  method: string,
  args: readonly unknown[],
): Extract<FromFrame, { t: "log" }> {
  const err = args.find((a) => a instanceof Error);
  const message = args.map((a) => (a instanceof Error ? a.message || a.name : attrString(a))).join(" ");
  return logLine(level, message, { console: method, ...(err?.stack ? { stack: err.stack } : {}) });
}

function attrString(v: unknown): string {
  if (typeof v === "string") return v;
  if (v instanceof Error) return v.stack ?? `${v.name}: ${v.message}`;
  if (v === null || typeof v !== "object") return String(v);
  try {
    return JSON.stringify(v, (_, x: unknown) => (typeof x === "bigint" ? x.toString() : x));
  } catch {
    return Object.prototype.toString.call(v); // a cycle, say: "[object Object]"
  }
}
