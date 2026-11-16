import { expect, test } from "bun:test";
import type { FromFrame } from "../src/bridge.ts";
import { logLine, makeLogger } from "../src/log.ts";

test("each level sends a line with its attrs as strings", () => {
  const sent: FromFrame[] = [];
  const log = makeLogger((m) => sent.push(m));
  log.debug("d");
  log.info("fetched", { n: 3, ok: true, at: { x: 1 }, big: 2n, none: undefined, s: "x" });
  log.warn("w");
  log.error("e");
  expect(sent.map((m) => (m.t === "log" ? m.level : m.t))).toEqual(["debug", "info", "warn", "error"]);
  expect(sent[1]).toEqual({
    t: "log",
    level: "info",
    message: "fetched",
    attrs: { n: "3", ok: "true", at: '{"x":1}', big: "2", none: "undefined", s: "x" },
  });
});

test("an Error brings its stack, unless one is given", () => {
  const e = new Error("boom");
  const l = logLine("error", e);
  expect(l.message).toBe("boom");
  expect(l.attrs.stack).toBe(e.stack ?? "");
  expect(logLine("error", e, { stack: "mine" }).attrs.stack).toBe("mine");
  expect(logLine("error", "failed", { err: e }).attrs.err).toContain("boom");
});

test("anything else thrown still makes a line", () => {
  expect(logLine("error", { code: 7 }).message).toBe('{"code":7}');
  expect(logLine("error", undefined).message).toBe("undefined");
  const cyclic: Record<string, unknown> = {};
  cyclic.self = cyclic;
  expect(logLine("info", "c", { cyclic }).attrs.cyclic).toBe("[object Object]");
});
