import { describe, expect, test } from "bun:test";
import { create, toBinary, type MessageInitShape } from "@bufbuild/protobuf";

import { CardCore, defineWidget, typeUrlOf, type WidgetDef } from "../src/index.ts";
import { decodeChecked } from "../src/checks.ts";
import type { FromFrame, Local as LocalBytes } from "../src/bridge.ts";
import {
  ActionSchema,
  ConfigSchema,
  DeltaSchema,
  StateSchema,
  type Delta,
  type State,
} from "./gen/testwidget/v1/testwidget_pb.ts";

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

describe("typed local value", () => {
  // Config stands in for a view's local message: its label has a max_len rule.
  function localHarness(local?: LocalBytes) {
    const saved: (LocalBytes | null)[] = [];
    const seen: string[] = [];
    let refuse: string | undefined;
    const def = defineWidget({
      state: StateSchema,
      local: ConfigSchema,
      mount: (ctx) => {
        seen.push(ctx.local.value.label); // read before the first update
        return { update() {} };
      },
    });
    const core = new CardCore(
      def,
      () => {},
      (local) => def.mount({ local } as never),
      async (l) => {
        if (refuse) throw new Error(refuse);
        saved.push(l);
      },
    );
    core.handle({ t: "init", cardId: "a/b", stateType: "testwidget.v1.State", deltaType: "", theme: {}, local });
    return { core, saved, seen, refuseWith: (e: string) => (refuse = e) };
  }
  const bytes = (label: string) => ({
    type: "testwidget.v1.Config",
    value: toBinary(ConfigSchema, create(ConfigSchema, { label })),
  });

  test("is ready at mount, from what the board saved", () => {
    const { core, seen } = localHarness(bytes("zoom2"));
    expect(seen).toEqual(["zoom2"]);
    expect(core.local.value.label).toBe("zoom2");
  });

  test("defaults when nothing was saved", () => {
    expect(localHarness().seen).toEqual([""]);
  });

  test("ignores a value saved as another type", () => {
    const other = { type: "testwidget.v1.Action", value: toBinary(ActionSchema, create(ActionSchema, { tap: 3n })) };
    expect(localHarness(other).seen).toEqual([""]);
  });

  test("ignores a saved value that breaks a rule or doesn't decode", () => {
    expect(localHarness(bytes("far too long")).seen).toEqual([""]);
    expect(localHarness({ type: "testwidget.v1.Config", value: new Uint8Array([0xff, 0xff]) }).seen).toEqual([""]);
  });

  test("set checks, keeps and saves the value with its type name", async () => {
    const { core, saved } = localHarness();
    await core.local.set(create(ConfigSchema, { label: "dark" }));
    expect(core.local.value.label).toBe("dark");
    expect(saved).toEqual([bytes("dark")]);
  });

  test("set refuses a value that breaks a rule and keeps the old one", async () => {
    const { core, saved } = localHarness(bytes("ok"));
    await expect(core.local.set(create(ConfigSchema, { label: "far too long" }))).rejects.toThrow();
    expect(core.local.value.label).toBe("ok");
    expect(saved).toEqual([]);
  });

  test("set rejects when the board refuses", async () => {
    const { core, refuseWith } = localHarness();
    refuseWith("storage quota exceeded");
    await expect(core.local.set(create(ConfigSchema, { label: "x" }))).rejects.toThrow("quota");
  });

  test("clear goes back to the defaults and forgets the saved value", async () => {
    const { core, saved } = localHarness(bytes("zoom2"));
    await core.local.clear();
    expect(core.local.value.label).toBe("");
    expect(saved).toEqual([null]);
  });

  test("a widget without a local type can't use it", async () => {
    const def = defineWidget({ state: StateSchema, mount: () => ({ update() {} }) });
    const core = new CardCore(def, () => {}, () => def.mount({} as never));
    expect(() => core.local.value).toThrow("declares no local type");
    await expect(core.local.set(create(StateSchema, {}))).rejects.toThrow("declares no local type");
  });
});
