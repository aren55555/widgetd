import { describe, expect, test } from "bun:test";
import { create, createFileRegistry, ScalarType, setExtension, type DescField } from "@bufbuild/protobuf";
import {
  FieldDescriptorProto_Label,
  FieldDescriptorProto_Type,
  FieldOptionsSchema,
  FileDescriptorProtoSchema,
} from "@bufbuild/protobuf/wkt";
import { field as fieldRules, FieldRulesSchema } from "@bufbuild/protovalidate/gen/buf/validate/validate_pb.js";

import {
  choiceOptions,
  clockMinutes,
  clockRules,
  enumLabel,
  formatClock,
  humanize,
  normalizePath,
  parseClock,
  parseScalar,
  sameChoices,
} from "./form.ts";

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

describe("choices", () => {
  const picks = [
    { value: "SJ", label: "San Jose Sharks" },
    { value: "ANA", label: "" },
  ];
  test("labels default to values", () => {
    expect(choiceOptions(picks, "SJ")).toEqual([
      { value: "SJ", label: "San Jose Sharks" },
      { value: "ANA", label: "ANA" },
    ]);
  });
  test("a saved value off the list stays", () => {
    expect(choiceOptions(picks, "SJS")[0]).toEqual({ value: "SJS", label: "SJS (current)" });
  });
  test("sameChoices", () => {
    const a = new Map([["team", picks]]);
    expect(sameChoices(a, new Map([["team", [...picks]]]))).toBe(true);
    expect(sameChoices(a, new Map())).toBe(false);
    expect(sameChoices(a, new Map([["team", picks.slice(1)]]))).toBe(false);
  });
});

/** A string field with this protovalidate pattern (or none). */
function stringField(pattern?: string): DescField {
  const options = create(FieldOptionsSchema);
  if (pattern !== undefined)
    setExtension(options, fieldRules, create(FieldRulesSchema, { type: { case: "string", value: { pattern } } }));
  const reg = createFileRegistry(
    create(FileDescriptorProtoSchema, {
      name: "t.proto",
      package: "t",
      syntax: "proto3",
      messageType: [
        {
          name: "M",
          field: [
            {
              name: "f",
              jsonName: "f",
              number: 1,
              type: FieldDescriptorProto_Type.STRING,
              label: FieldDescriptorProto_Label.OPTIONAL,
              options,
            },
          ],
        },
      ],
    }),
    () => undefined,
  );
  return reg.getMessage("t.M")!.fields[0];
}

describe("clock fields", () => {
  test("a pattern for clock times makes a clock field", () => {
    // The calendar's work_start and work_end.
    expect(clockRules(stringField("^(([01][0-9]|2[0-3]):[0-5][0-9])?$"))).toEqual({ unset: true, midnight: false });
    expect(clockRules(stringField("^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)?$"))).toEqual({
      unset: true,
      midnight: true,
    });
    expect(clockRules(stringField("^([01][0-9]|2[0-3]):[0-5][0-9]$"))).toEqual({ unset: false, midnight: false });
  });
  test("anything else doesn't", () => {
    expect(clockRules(stringField())).toBeUndefined();
    expect(clockRules(stringField(""))).toBeUndefined();
    expect(clockRules(stringField("^.*$"))).toBeUndefined();
    expect(clockRules(stringField("^[0-9]{2}:[0-9]{2}$"))).toBeUndefined(); // allows 25:00
    expect(clockRules(stringField("^([01][0-9]|2[0-3]):[0-2][0-9]$"))).toBeUndefined(); // not every minute
    expect(clockRules(stringField("(("))).toBeUndefined();
  });
  test("parse and format", () => {
    expect(parseClock("08:15")).toEqual({ hour: 8, minute: 15 });
    expect(parseClock("24:00")).toEqual({ hour: 24, minute: 0 });
    expect(parseClock("")).toBeUndefined();
    expect(parseClock("8:15")).toBeUndefined();
    expect(formatClock(8, 5)).toBe("08:05");
  });
  test("minutes keep an off-grid saved minute", () => {
    expect(clockMinutes(undefined)).toHaveLength(12);
    expect(clockMinutes(30)).toHaveLength(12);
    const m = clockMinutes(13);
    expect(m).toHaveLength(13);
    expect(m.slice(2, 5)).toEqual([10, 13, 15]);
  });
});
