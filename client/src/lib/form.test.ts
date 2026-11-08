import { describe, expect, test } from "bun:test";
import { ScalarType } from "@bufbuild/protobuf";

import { enumLabel, humanize, normalizePath, parseScalar } from "./form.ts";

describe("humanize", () => {
  test("snake and camel case read as words", () => {
    expect(humanize("time_zone")).toBe("Time zone");
    expect(humanize("maxItems")).toBe("Max items");
    expect(humanize("label")).toBe("Label");
  });
});

describe("enumLabel", () => {
  test("drops the shared prefix", () => {
    const e = { sharedPrefix: "CURRENCY_" } as never;
    expect(enumLabel(e, "CURRENCY_USD")).toBe("USD");
  });
});

describe("parseScalar", () => {
  test("32-bit integers are range checked", () => {
    expect(parseScalar(ScalarType.INT32, "42")).toEqual({ ok: true, value: 42 });
    expect(parseScalar(ScalarType.INT32, "3000000000").ok).toBe(false);
    expect(parseScalar(ScalarType.UINT32, "-1").ok).toBe(false);
    expect(parseScalar(ScalarType.INT32, "4.2").ok).toBe(false);
  });

  test("64-bit integers are bigints", () => {
    expect(parseScalar(ScalarType.INT64, "9007199254740993")).toEqual({ ok: true, value: 9007199254740993n });
    expect(parseScalar(ScalarType.UINT64, "-1").ok).toBe(false);
  });

  test("floats must be finite", () => {
    expect(parseScalar(ScalarType.DOUBLE, "1.5")).toEqual({ ok: true, value: 1.5 });
    expect(parseScalar(ScalarType.DOUBLE, "abc").ok).toBe(false);
  });

  test("empty means the zero value", () => {
    expect(parseScalar(ScalarType.INT32, "")).toEqual({ ok: true, value: 0 });
    expect(parseScalar(ScalarType.STRING, "")).toEqual({ ok: true, value: "" });
  });
});

test("paths drop indexes so both validators agree", () => {
  expect(normalizePath("teams[0].league")).toBe("teams.league");
});
