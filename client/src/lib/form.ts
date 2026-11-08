// A settings form built from nothing but a widget's Config descriptor: every
// field gets an input that fits its type, every protovalidate rule is
// checked as you type, and the result is an ordinary Config message. So most
// widgets ship no settings UI at all, and the one a phone shows for a widget
// installed yesterday works the same as for one built in.
//
// The form edits a protobuf-es message object in place. The DOM side is in
// buildForm; the parsing and naming it relies on are pure, and tested.

import { create, ScalarType, type DescEnum, type DescField, type DescMessage, type DescOneof, type Message } from "@bufbuild/protobuf";
import { pathToString } from "@bufbuild/protobuf/reflect";
import { createValidator } from "@bufbuild/protovalidate";

/** "time_zone" → "Time zone". */
export function humanize(name: string): string {
  const s = name.replace(/_/g, " ").replace(/([a-z0-9])([A-Z])/g, "$1 $2").toLowerCase().trim();
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
      if (unsigned ? n < 0n || n > 2n ** 64n - 1n : n < -(2n ** 63n) || n > 2n ** 63n - 1n) return { ok: false, error: "out of range" };
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
      if (unsigned ? n < 0 || n > 4294967295 : n < -2147483648 || n > 2147483647) return { ok: false, error: "out of range" };
      return { ok: true, value: n };
    }
  }
}

/** Field paths the way both validators can agree on: "teams.league", no indexes. */
export const normalizePath = (p: string) => p.replace(/\[[^\]]*\]/g, "");

// ---- DOM ----

type El = HTMLElement;

function el(tag: string, attrs: Record<string, string> = {}, ...kids: (Node | string)[]): El {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
  e.append(...kids);
  return e;
}

type Ctx = {
  /** Called after any edit. */
  changed: () => void;
  /** Where to show an error for a field path. */
  errorSlots: Map<string, El>;
};

/**
 * Render a form for `msg` (a message of `desc`) into `root`. Returns
 * validate(), which checks the message as it now stands and shows each
 * violation beside its field, returning whether it's valid.
 */
export function buildForm(root: El, desc: DescMessage, msg: Message, onChange: () => void) {
  const errorSlots = new Map<string, El>();
  const parseErrors = new Set<string>();
  const ctx: Ctx = { changed: () => onChange(), errorSlots };
  root.replaceChildren(messageFields(desc, msg as unknown as Record<string, unknown>, "", ctx, parseErrors));
  const validator = createValidator();

  function validate(serverViolations?: { field: string; message: string }[]): boolean {
    for (const slot of errorSlots.values()) slot.textContent = "";
    const violations =
      serverViolations ??
      (() => {
        const r = validator.validate(desc, msg as never);
        return r.kind === "valid" ? [] : r.kind === "invalid" ? r.violations.map((v) => ({ field: pathToString(v.field), message: v.message })) : [{ field: "", message: r.error.message }];
      })();
    for (const v of violations) {
      const slot = errorSlots.get(normalizePath(v.field)) ?? errorSlots.get("");
      if (slot) slot.textContent = slot.textContent ? `${slot.textContent}; ${v.message}` : v.message;
    }
    return violations.length === 0 && parseErrors.size === 0;
  }
  return { validate };
}

function messageFields(desc: DescMessage, obj: Record<string, unknown>, path: string, ctx: Ctx, parseErrors: Set<string>): El {
  const box = el("div", { class: "fields" });
  const whole = el("p", { class: "field-error", role: "alert" });
  ctx.errorSlots.set(path, whole);
  box.append(whole);
  const seenOneofs = new Set<DescOneof>();
  for (const f of desc.fields) {
    if (f.oneof) {
      if (seenOneofs.has(f.oneof)) continue;
      seenOneofs.add(f.oneof);
      box.append(oneofField(f.oneof, obj, path, ctx, parseErrors));
      continue;
    }
    box.append(field(f, obj, path, ctx, parseErrors));
  }
  return box;
}

const join = (path: string, name: string) => (path ? `${path}.${name}` : name);

function field(f: DescField, obj: Record<string, unknown>, path: string, ctx: Ctx, parseErrors: Set<string>): El {
  const p = join(path, f.name);
  const err = el("p", { class: "field-error", role: "alert" });
  ctx.errorSlots.set(p, err);
  const label = humanize(f.name);
  const key = f.localName;

  if (f.fieldKind === "map") {
    return el("div", { class: "field" }, el("span", { class: "label" }, label), el("p", { class: "muted" }, "Maps can't be edited here yet."));
  }
  if (f.fieldKind === "list") {
    const list = (obj[key] as unknown[]) ?? (obj[key] = []);
    const rows = el("div", { class: "rows" });
    const render = () => {
      rows.replaceChildren();
      list.forEach((_, i) => {
        const holder = { v: list[i] } as Record<string, unknown>;
        const item = itemEditor(f, holder, "v", `${p}`, ctx, parseErrors, () => {
          list[i] = holder.v;
        });
        const remove = el("button", { type: "button", class: "small secondary" }, "Remove");
        remove.addEventListener("click", () => {
          list.splice(i, 1);
          render();
          ctx.changed();
        });
        rows.append(el("div", { class: "row" }, item, remove));
      });
    };
    const add = el("button", { type: "button", class: "small secondary" }, `Add ${label.toLowerCase()}`);
    add.addEventListener("click", () => {
      list.push(defaultItem(f));
      render();
      ctx.changed();
    });
    render();
    return el("fieldset", { class: "field" }, el("legend", {}, label), rows, add, err);
  }
  return el("div", { class: "field" }, itemEditor(f, obj, key, p, ctx, parseErrors, () => {}, label), err);
}

function defaultItem(f: DescField): unknown {
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

/** One value: a scalar input, an enum select, or a nested message. */
function itemEditor(
  f: DescField,
  holder: Record<string, unknown>,
  key: string,
  path: string,
  ctx: Ctx,
  parseErrors: Set<string>,
  commit: () => void,
  label?: string,
): El {
  const kind = f.fieldKind === "list" ? f.listKind : f.fieldKind;
  const wrap = (input: El) => (label ? el("label", {}, el("span", { class: "label" }, label), input) : input);

  if (kind === "message") {
    const md = f.message!;
    let m = holder[key] as Record<string, unknown> | undefined;
    if (!m) {
      m = create(md) as unknown as Record<string, unknown>;
      holder[key] = m;
      commit();
    }
    const inner = messageFields(md, m, path, ctx, parseErrors);
    return label ? el("fieldset", {}, el("legend", {}, label), inner) : inner;
  }
  if (kind === "enum") {
    const e = f.enum!;
    const sel = el("select") as HTMLSelectElement;
    for (const v of e.values) {
      const o = el("option", { value: String(v.number) }, v.number === 0 ? `(${enumLabel(e, v.name).toLowerCase() || "default"})` : enumLabel(e, v.name)) as HTMLOptionElement;
      if (holder[key] === v.number) o.selected = true;
      sel.append(o);
    }
    sel.addEventListener("change", () => {
      holder[key] = Number(sel.value);
      commit();
      ctx.changed();
    });
    return wrap(sel);
  }
  const t = f.scalar!;
  if (t === ScalarType.BOOL) {
    const box = el("input", { type: "checkbox" }) as HTMLInputElement;
    box.checked = holder[key] === true;
    box.addEventListener("change", () => {
      holder[key] = box.checked;
      commit();
      ctx.changed();
    });
    return label ? el("label", { class: "check" }, box, el("span", { class: "label" }, label)) : box;
  }
  if (t === ScalarType.BYTES) {
    return wrap(el("p", { class: "muted" }, "Can't be edited here."));
  }
  const numeric = t !== ScalarType.STRING;
  const input = el("input", { type: "text", inputmode: numeric ? "decimal" : "text", autocomplete: "off" }) as HTMLInputElement;
  input.value = holder[key] === undefined ? "" : String(holder[key]);
  input.addEventListener("input", () => {
    const r = parseScalar(t, input.value);
    if (r.ok) {
      holder[key] = r.value;
      parseErrors.delete(path);
      input.removeAttribute("aria-invalid");
      commit();
    } else {
      parseErrors.add(path);
      input.setAttribute("aria-invalid", "true");
      const slot = ctx.errorSlots.get(path);
      if (slot) slot.textContent = r.error;
    }
    ctx.changed();
  });
  return wrap(input);
}

/** A oneof: pick which field is set, then edit it. */
function oneofField(o: DescOneof, obj: Record<string, unknown>, path: string, ctx: Ctx, parseErrors: Set<string>): El {
  const p = join(path, o.name);
  const err = el("p", { class: "field-error", role: "alert" });
  ctx.errorSlots.set(p, err);
  const cur = obj[o.localName] as { case?: string; value?: unknown } | undefined;
  const sel = el("select") as HTMLSelectElement;
  sel.append(el("option", { value: "" }, "(none)"));
  for (const f of o.fields) {
    const opt = el("option", { value: f.localName }, humanize(f.name)) as HTMLOptionElement;
    if (cur?.case === f.localName) opt.selected = true;
    sel.append(opt);
  }
  const body = el("div");
  const render = () => {
    body.replaceChildren();
    const c = obj[o.localName] as { case?: string; value?: unknown };
    const f = o.fields.find((x) => x.localName === c?.case);
    if (!f) return;
    const holder = c as Record<string, unknown>;
    body.append(itemEditor(f, holder, "value", join(path, f.name), ctx, parseErrors, () => {}));
  };
  sel.addEventListener("change", () => {
    const f = o.fields.find((x) => x.localName === sel.value);
    if (!f) obj[o.localName] = { case: undefined };
    else {
      const v = f.fieldKind === "message" ? create(f.message) : f.fieldKind === "enum" ? 0 : (parseScalar(f.scalar!, "") as { value: unknown }).value ?? "";
      obj[o.localName] = { case: f.localName, value: v };
    }
    render();
    ctx.changed();
  });
  render();
  return el("fieldset", { class: "field" }, el("legend", {}, humanize(o.name)), sel, body, err);
}
