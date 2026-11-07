import { describe, expect, test } from "bun:test";
import { create, toBinary, type MessageInitShape } from "@bufbuild/protobuf";

import { CardCore, defineWidget, typeUrlOf, type WidgetDef } from "../src/index.ts";
import { decodeChecked } from "../src/checks.ts";
import type { FromFrame } from "../src/bridge.ts";
import { DeltaSchema, StateSchema, type Delta, type State } from "./gen/testwidget/v1/testwidget_pb.ts";

const cursor = (seq: number) => ({ epoch: "1", seq: String(seq) });

function harness() {
  const sent: FromFrame[] = [];
  const renders: { state: State; delta?: Delta }[] = [];
  const def: WidgetDef<typeof StateSchema, typeof DeltaSchema> = defineWidget({
    state: StateSchema,
    delta: DeltaSchema,
    apply: (s, d) => {
      s.values.push(d.value);
      return s;
    },
    mount: () => ({ update: (state, delta) => renders.push({ state, delta }) }),
  });
  const core = new CardCore(def, (m) => sent.push(m), def.mount.bind(def, {} as never));
  core.handle({ t: "init", cardId: "a/b", stateType: "testwidget.v1.State", deltaType: "testwidget.v1.Delta", theme: {} });
  return { core, sent, renders };
}

const snapshot = (s: MessageInitShape<typeof StateSchema>, seq = 1) => ({
  t: "snapshot" as const,
  typeUrl: typeUrlOf(StateSchema),
  value: toBinary(StateSchema, create(StateSchema, s)),
  cursor: cursor(seq),
});
const delta = (value: bigint, seq: number) => ({
  t: "delta" as const,
  typeUrl: typeUrlOf(DeltaSchema),
  value: toBinary(DeltaSchema, create(DeltaSchema, { value })),
  cursor: cursor(seq),
});

describe("CardCore", () => {
  test("accepts a snapshot, renders it and checkpoints it", () => {
    const { core, sent, renders } = harness();
    core.handle(snapshot({ values: [1n] }));
    expect(renders.at(-1)?.state.values).toEqual([1n]);
    expect(sent.at(-1)).toMatchObject({ t: "checkpoint", cursor: cursor(1) });
  });

  test("applies deltas on top of the accepted state", () => {
    const { core, renders } = harness();
    core.handle(snapshot({ values: [1n] }));
    core.handle(delta(2n, 2));
    expect(renders.at(-1)?.state.values).toEqual([1n, 2n]);
    expect(renders.at(-1)?.delta?.value).toBe(2n);
  });

  test("refuses a delta that breaks its rule and keeps the last good state", () => {
    const { core, sent } = harness();
    core.handle(snapshot({ values: [1n] }));
    core.handle(delta(-1n, 2));
    expect(sent.at(-1)).toMatchObject({ t: "fault", cursor: cursor(2) });
    expect(core.current?.values).toEqual([1n]);
  });

  test("refuses a payload of the wrong type", () => {
    const { core, sent } = harness();
    core.handle({ ...snapshot({}), typeUrl: typeUrlOf(DeltaSchema) });
    expect(sent.at(-1)?.t).toBe("fault");
    expect(core.current).toBeUndefined();
  });

  test("refuses a snapshot that breaks a rule", () => {
    const { core, sent } = harness();
    core.handle(snapshot({ label: "far too long" }));
    expect(sent.at(-1)?.t).toBe("fault");
  });

  test("refuses a card whose types aren't the view's", () => {
    const sent: FromFrame[] = [];
    const def = defineWidget({ state: StateSchema, mount: () => ({ update() {} }) });
    const core = new CardCore(def, (m) => sent.push(m), () => ({ update() {} }));
    core.handle({ t: "init", cardId: "x", stateType: "other.State", deltaType: "", theme: {} });
    expect(sent.at(-1)?.t).toBe("fault");
  });

  test("draws stored state immediately on init", () => {
    const renders: State[] = [];
    const def = defineWidget({ state: StateSchema, mount: () => ({ update: (s) => renders.push(s) }) });
    const core = new CardCore(def, () => {}, () => ({ update: (s) => renders.push(s) }));
    core.handle({
      t: "init",
      cardId: "x",
      stateType: "testwidget.v1.State",
      deltaType: "",
      theme: {},
      stored: { state: toBinary(StateSchema, create(StateSchema, { values: [7n] })), cursor: cursor(3) },
    });
    expect(renders[0]?.values).toEqual([7n]);
  });
});

describe("decodeChecked", () => {
  test("refuses unknown fields", () => {
    // Field 99, varint 1: not in State.
    const bytes = new Uint8Array([0x98, 0x06, 0x01]);
    const r = decodeChecked(StateSchema, typeUrlOf(StateSchema), bytes);
    expect(r.ok).toBe(false);
  });

  test("refuses bytes that don't decode", () => {
    const r = decodeChecked(StateSchema, typeUrlOf(StateSchema), new Uint8Array([0xff, 0xff, 0xff]));
    expect(r.ok).toBe(false);
  });
});
