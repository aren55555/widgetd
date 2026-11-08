import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";

import { CardStore, VIEW_QUOTA, type KVBackend } from "./store.ts";
import { deltaFollows, gridSpan, instanceOf, placeholderFor, staleCards, usableStored } from "./sync.ts";
import { CursorSchema, InstanceStatus_State, PayloadSchema } from "../gen/widgetd/v1/stream_pb.ts";

function memory(): KVBackend {
  const m = new Map<string, string>();
  return {
    getItem: (k) => m.get(k) ?? null,
    setItem: (k, v) => void m.set(k, v),
    removeItem: (k) => void m.delete(k),
    key: (i) => [...m.keys()][i] ?? null,
    get length() {
      return m.size;
    },
  };
}

const stored = (seq: string) => ({
  digest: "d1",
  stateType: "x.State",
  cursor: { epoch: "1", seq },
  state: new Uint8Array([1, 2, 3]),
});

describe("CardStore", () => {
  test("round-trips state and lists it for resume", () => {
    const s = new CardStore(memory());
    s.save("i/a", stored("4"));
    expect(s.load("i/a")).toEqual(stored("4"));
    expect(s.saved().map((x) => x.cardId)).toEqual(["i/a"]);
  });

  test("forget removes state, and with view, the card's preferences", () => {
    const s = new CardStore(memory());
    s.save("i/a", stored("1"));
    s.viewSet("i/a", "zoom", "2");
    s.viewSet("i/b", "zoom", "3");
    s.forget("i/a", false);
    expect(s.load("i/a")).toBeUndefined();
    expect(s.viewGet("i/a", "zoom")).toBe("2");
    s.forget("i/a", true);
    expect(s.viewGet("i/a", "zoom")).toBeUndefined();
    expect(s.viewGet("i/b", "zoom")).toBe("3");
  });

  test("a card's preferences are namespaced and quota-limited", () => {
    const s = new CardStore(memory());
    s.viewSet("i/a", "k", "v");
    expect(s.viewGet("i/b", "k")).toBeUndefined();
    expect(() => s.viewSet("i/a", "big", "x".repeat(VIEW_QUOTA))).toThrow();
  });

  test("garbage in storage reads as nothing", () => {
    const ls = memory();
    ls.setItem("widgetd:state:i/a", "{not json");
    expect(new CardStore(ls).load("i/a")).toBeUndefined();
  });
});

describe("sync", () => {
  const payload = (prevSeq: bigint) =>
    create(PayloadSchema, { prev: create(CursorSchema, { epoch: 1n, seq: prevSeq }) });

  test("a delta follows only the cursor it was built on", () => {
    expect(deltaFollows({ epoch: "1", seq: "4" }, payload(4n))).toBe(true);
    expect(deltaFollows({ epoch: "1", seq: "3" }, payload(4n))).toBe(false);
    expect(deltaFollows(undefined, payload(4n))).toBe(false);
  });

  test("stored state is used only for the same build and type", () => {
    expect(usableStored(stored("1"), "d1", "x.State")).toBeDefined();
    expect(usableStored(stored("1"), "d2", "x.State")).toBeUndefined();
    expect(usableStored(stored("1"), "d1", "y.State")).toBeUndefined();
  });

  test("grid spans are clamped", () => {
    expect(gridSpan(4, 2, 3)).toEqual({ gridColumn: "span 3", gridRow: "span 2" });
    expect(gridSpan(0, 0, 3)).toEqual({ gridColumn: "span 1", gridRow: "span 1" });
  });

  test("cards the burst didn't mount are stale", () => {
    expect(staleCards(["a", "b"], new Set(["b"]))).toEqual(["a"]);
  });
});

describe("placeholders", () => {
  test("a running instance needs none; the others say why", () => {
    expect(placeholderFor(InstanceStatus_State.RUNNING, "").show).toBe(false);
    expect(placeholderFor(InstanceStatus_State.NEEDS_CONFIG, "")).toEqual({ show: true, text: "Not set up yet.", setUp: true });
    expect(placeholderFor(InstanceStatus_State.INVALID_CONFIG, "")).toMatchObject({ show: true, setUp: true });
    expect(placeholderFor(InstanceStatus_State.FAULTED, "boom")).toMatchObject({ show: true, setUp: false, text: "Stopped: boom. Retrying…" });
  });

  test("a card's instance is its id's first segment", () => {
    expect(instanceOf("btc/spiral")).toBe("btc");
    expect(instanceOf("sports-x7/game/123")).toBe("sports-x7");
    expect(instanceOf("bare")).toBe("");
  });
});
