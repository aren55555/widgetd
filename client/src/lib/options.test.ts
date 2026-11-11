import { describe, expect, test } from "bun:test";
import { create, equals, fromJson, toBinary, type JsonValue } from "@bufbuild/protobuf";
import { base64Encode } from "@bufbuild/protobuf/wire";

import { BoardOptionsSchema } from "../gen/widgetd/v1/board_pb.ts";
import fixture from "./options.fixture.json";
import { validated } from "@widgetd/sdk/checks";
import {
  checkRules,
  DEFAULT_COLS,
  DEFAULT_STALE_AFTER_MS,
  encodeBoardOptions,
  readBoardOptions,
  resolveLayout,
} from "./options.ts";

const enc = (json: JsonValue) => base64Encode(toBinary(BoardOptionsSchema, fromJson(BoardOptionsSchema, json)), "url");

describe("BoardOptions fixture", () => {
  for (const { o, json } of fixture) {
    test(`encodes ${JSON.stringify(json)} as ${o}`, () => {
      expect(encodeBoardOptions(fromJson(BoardOptionsSchema, json as JsonValue))).toBe(o);
    });
    test(`decodes ${o}`, () => {
      const r = readBoardOptions(`?o=${o}`);
      expect(r.error).toBeUndefined();
      expect(equals(BoardOptionsSchema, r.options, fromJson(BoardOptionsSchema, json as JsonValue))).toBe(true);
    });
  }

  test("the doc's example", () => {
    expect(fixture.find((f) => f.o === "CAMSAgg8")?.json).toEqual({ cols: 3, staleAfter: "60s" });
  });
});

describe("readBoardOptions", () => {
  test("an empty message writes no o", () => {
    expect(encodeBoardOptions(create(BoardOptionsSchema))).toBe("");
  });

  test("no o is the empty message, without an error", () => {
    const r = readBoardOptions("?x=1");
    expect(r.error).toBeUndefined();
    expect(r.options.cols).toBeUndefined();
  });

  const ignored: Array<[string, string]> = [
    ["not base64url", "CAM+Agg8"],
    ["bytes that don't decode", "_w"],
    ["cols 0", enc({ cols: 0 })],
    ["cols 13", enc({ cols: 13 })],
    ["stale_after 1s", enc({ staleAfter: "1s" })],
    ["a bad rule next to a good field", enc({ cols: 13, staleAfter: "60s" })],
  ];
  for (const [name, o] of ignored) {
    test(`ignores ${name} as a whole`, () => {
      const r = readBoardOptions(`?o=${o}`);
      expect(r.error).toBeString();
      expect(equals(BoardOptionsSchema, r.options, create(BoardOptionsSchema))).toBe(true);
    });
  }

  test("keeps fields it doesn't know (a newer contract)", () => {
    // cols = 3, then field 99 (varint 1).
    const o = base64Encode(new Uint8Array([0x08, 0x03, 0x98, 0x06, 0x01]), "url");
    const r = readBoardOptions(`?o=${o}`);
    expect(r.error).toBeUndefined();
    expect(r.options.cols).toBe(3);
  });
});

describe("checkRules", () => {
  test("agrees with protovalidate on board.proto's rules", () => {
    const cases: JsonValue[] = [
      {},
      ...[0, 1, 2, 11, 12, 13, 4294967295].map((cols) => ({ cols })),
      ...["-5s", "0s", "4s", "4.999999999s", "5s", "5.000000001s", "3600s"].map((staleAfter) => ({ staleAfter })),
      { cols: 4, staleAfter: "1s" },
      { prefs: { locale: "anything" } },
    ];
    for (const json of cases) {
      const m = fromJson(BoardOptionsSchema, json);
      expect({ json, ok: checkRules(m) === undefined }).toEqual({ json, ok: validated(BoardOptionsSchema, m).ok });
    }
  });
});

describe("resolveLayout", () => {
  const opts = (json: JsonValue) => fromJson(BoardOptionsSchema, json);

  test("URL over attribute over default", () => {
    expect(resolveLayout(opts({ cols: 3 }), "6")).toMatchObject({ cols: 3, colsSource: "set" });
    expect(resolveLayout(opts({}), "6")).toMatchObject({ cols: 6, colsSource: "attribute" });
    expect(resolveLayout(opts({}), "nonsense")).toMatchObject({ cols: DEFAULT_COLS, colsSource: "default" });
    expect(resolveLayout(opts({}), null)).toMatchObject({ cols: DEFAULT_COLS, colsSource: "default" });
  });

  test("stale_after", () => {
    expect(resolveLayout(opts({ staleAfter: "90.5s" }), null)).toMatchObject({
      staleAfterMs: 90_500,
      staleAfterSource: "set",
    });
    expect(resolveLayout(opts({}), null)).toMatchObject({
      staleAfterMs: DEFAULT_STALE_AFTER_MS,
      staleAfterSource: "default",
    });
  });
});
