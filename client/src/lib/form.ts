// A settings form built from nothing but a widget's Config descriptor: every
// field gets an input that fits its type, every protovalidate rule is
// checked as you type, and the result is an ordinary Config message. So most
// widgets ship no settings UI at all, and the one a phone shows for a widget
// installed yesterday works the same as for one built in.
//
// The form edits a protobuf-es message object in place. The UI is the Solid
// component in ConfigForm.tsx; the parsing and naming it relies on are here,
// pure, and tested.

import { create, ScalarType, type DescEnum, type DescField } from "@bufbuild/protobuf";

/** "time_zone" → "Time zone". */
export function humanize(name: string): string {
  const s = name
    .replace(/_/g, " ")
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .toLowerCase()
    .trim();
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** "CURRENCY_USD" in enum Currency → "USD". */
export function enumLabel(e: DescEnum, valueName: string): string {
  const prefix = e.sharedPrefix ? e.sharedPrefix : "";
  const bare = prefix && valueName.startsWith(prefix) ? valueName.slice(prefix.length) : valueName;
  return bare.replace(/^_/, "");
}

export type Parsed = { ok: true; value: unknown } | { ok: false; error: string };

/** An input's text as a value of the scalar type, or why it isn't one. */
export function parseScalar(t: ScalarType, text: string): Parsed {
  const s = text.trim();
  switch (t) {
    case ScalarType.STRING:
      return { ok: true, value: text };
    case ScalarType.BOOL:
      return { ok: true, value: s === "true" };
    case ScalarType.DOUBLE:
    case ScalarType.FLOAT: {
      if (s === "") return { ok: true, value: 0 };
      const n = Number(s);
      return Number.isFinite(n) ? { ok: true, value: n } : { ok: false, error: "not a number" };
    }
    case ScalarType.INT64:
    case ScalarType.SINT64:
    case ScalarType.SFIXED64:
    case ScalarType.UINT64:
    case ScalarType.FIXED64: {
      if (s === "") return { ok: true, value: 0n };
      if (!/^-?\d+$/.test(s)) return { ok: false, error: "not a whole number" };
      const n = BigInt(s);
      const unsigned = t === ScalarType.UINT64 || t === ScalarType.FIXED64;
      if (unsigned ? n < 0n || n > 2n ** 64n - 1n : n < -(2n ** 63n) || n > 2n ** 63n - 1n)
        return { ok: false, error: "out of range" };
      return { ok: true, value: n };
    }
    case ScalarType.BYTES:
      return { ok: false, error: "bytes can't be edited here" };
    default: {
      // 32-bit integers
      if (s === "") return { ok: true, value: 0 };
      if (!/^-?\d+$/.test(s)) return { ok: false, error: "not a whole number" };
      const n = Number(s);
      const unsigned = t === ScalarType.UINT32 || t === ScalarType.FIXED32;
      if (unsigned ? n < 0 || n > 4294967295 : n < -2147483648 || n > 2147483647)
        return { ok: false, error: "out of range" };
      return { ok: true, value: n };
    }
  }
}

/** Field paths the way both validators can agree on: "teams.league", no indexes. */
export const normalizePath = (p: string) => p.replace(/\[[^\]]*\]/g, "");

export interface FieldChoice {
  value: string;
  label: string;
}

/** Pick-lists by field path ("team", "location.city"), as the widget offered them. */
export type Choices = ReadonlyMap<string, readonly FieldChoice[]>;

/** Write-only fields: which they are, which have a stored value, and which the user asked to remove. */
export interface Secrets {
  fields: ReadonlySet<string>;
  set: ReadonlySet<string>;
  clear: Set<string>;
}

/** A new list item: an empty message, the first enum value, or a scalar's zero. */
export function defaultItem(f: DescField): unknown {
  if (f.fieldKind !== "list") return undefined;
  switch (f.listKind) {
    case "message":
      return create(f.message);
    case "enum":
      return f.enum.values[0]?.number ?? 0;
    default:
      return parseScalar(f.scalar, "").ok ? (parseScalar(f.scalar, "") as { value: unknown }).value : "";
  }
}

/** The options a pick-list shows: the widget's, plus the current value if it's not among them. */
export function choiceOptions(picks: readonly FieldChoice[], current: string): FieldChoice[] {
  const out = picks.map((p) => ({ value: p.value, label: p.label || p.value }));
  if (current && !picks.some((p) => p.value === current))
    out.unshift({ value: current, label: `${current} (current)` });
  return out;
}

/** Whether two sets of choices differ, so the form is only redrawn when they do. */
export function sameChoices(a: Choices, b: Choices): boolean {
  if (a.size !== b.size) return false;
  for (const [k, v] of a) {
    const w = b.get(k);
    if (w?.length !== v.length || v.some((c, i) => c.value !== w[i].value || c.label !== w[i].label)) return false;
  }
  return true;
}
