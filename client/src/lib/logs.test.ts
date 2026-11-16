import { expect, test } from "bun:test";
import { ViewLog_Level } from "../gen/widgetd/v1/stream_pb.ts";
import { LOG_LIMITS, LogQueue } from "./logs.ts";

test("well-formed lines are held in order, cut to the host's caps", () => {
  const q = new LogQueue();
  q.push("one/main", { level: "error", message: "boom", attrs: { stack: "at x", n: 3 } }, 1_700_000_000_123);
  q.push("one/main", { level: "info", message: "x".repeat(LOG_LIMITS.line + 10) });
  const many = Object.fromEntries(Array.from({ length: 20 }, (_, i) => [`k${String(i)}`, "v"]));
  q.push("two/main", { level: "debug", message: "m", attrs: many });
  const b = q.take();
  expect(b?.dropped).toBe(0);
  expect(b?.logs.map((l) => [l.cardId, l.level])).toEqual([
    ["one/main", ViewLog_Level.ERROR],
    ["one/main", ViewLog_Level.INFO],
    ["two/main", ViewLog_Level.DEBUG],
  ]);
  // Only string attrs: the SDK sends nothing else, so anything else is junk.
  expect(b?.logs[0]?.attrs).toEqual({ stack: "at x" });
  expect(b?.logs[0]?.time?.seconds).toBe(1_700_000_000n);
  expect(b?.logs[0]?.time?.nanos).toBe(123_000_000);
  expect(b?.logs[1]?.message.length).toBe(LOG_LIMITS.line);
  expect(Object.keys(b?.logs[2]?.attrs ?? {}).length).toBe(LOG_LIMITS.attrs);
  expect(q.take()).toBeUndefined();
});

test("malformed lines are ignored", () => {
  const q = new LogQueue();
  q.push("c", { level: "loud", message: "x" });
  q.push("c", { level: "info", message: 3 });
  q.push("c", {});
  expect(q.take()).toBeUndefined();
});

test("past its cap the queue counts what it drops, and batches stay within the host's", () => {
  const q = new LogQueue(150);
  for (let i = 0; i < 160; i++) q.push("c", { level: "info", message: String(i) });
  const first = q.take();
  expect(first?.logs.length).toBe(LOG_LIMITS.batch);
  expect(first?.dropped).toBe(10);
  const second = q.take();
  expect(second?.logs.length).toBe(50);
  expect(second?.logs[0]?.message).toBe("100");
  expect(second?.dropped).toBe(0);
  expect(q.take()).toBeUndefined();
});
