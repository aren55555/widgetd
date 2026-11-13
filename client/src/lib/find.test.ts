import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";

import { CardRefSchema, InstanceInfoSchema } from "../gen/widgetd/v1/admin_pb.ts";
import { instanceMatches, normalizeQuery, shortIdMatches } from "./find.ts";

const inst = create(InstanceInfoSchema, {
  id: "clock-ab12cd",
  widget: "clock",
  label: "Kitchen",
  cards: [create(CardRefSchema, { id: "clock-ab12cd/main", shortId: "3f9a0c1" })],
});

describe("find", () => {
  test("normalizes what was typed", () => {
    expect(normalizeQuery("  #3F9A ")).toBe("3f9a");
  });

  test("matches a short id by prefix", () => {
    expect(shortIdMatches("3f9a0c1", "3f9a")).toBe(true);
    expect(shortIdMatches("3f9a0c1", "9a0")).toBe(false);
    expect(shortIdMatches("3f9a0c1", "")).toBe(false);
  });

  test("matches an instance by a card's short id, or its names", () => {
    expect(instanceMatches(inst, "Clock", "")).toBe(true);
    expect(instanceMatches(inst, "Clock", "3f9a0c1")).toBe(true);
    expect(instanceMatches(inst, "Clock", "kitchen")).toBe(true);
    expect(instanceMatches(inst, "Clock", "ab12")).toBe(true);
    expect(instanceMatches(inst, "Clock", "0000000")).toBe(false);
  });
});
