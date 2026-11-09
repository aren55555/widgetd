// The settings form as Solid components, built from a widget's Config
// descriptor (see form.ts for the idea and the pure parts).
//
// The form edits a plain protobuf-es message in place: it isn't a Solid
// store, so toBinary sees an ordinary message. What changes on screen is
// held in signals: the errors, which list items exist, which oneof case is
// picked, and the widget's pick-lists.

import { create, ScalarType, type DescField, type DescMessage, type DescOneof, type Message } from "@bufbuild/protobuf";
import { pathToString } from "@bufbuild/protobuf/reflect";
import { createValidator } from "@bufbuild/protovalidate";
import { createSignal, Show, untrack, type Accessor, type JSX } from "solid-js";

import { choiceOptions, defaultItem, enumLabel, humanize, normalizePath, parseScalar, type Choices, type FieldChoice, type Secrets } from "./form.ts";
import * as ui from "./ui.ts";

type Rec = Record<string, unknown>;

type Ctx = {
  /** Called after any edit. */
  changed: () => void;
  choices: Accessor<Choices>;
  secrets: Secrets;
  /** Paths that show their own errors; a violation anywhere else shows at the top. */
  known: Set<string>;
  /** The errors to show for a path (reactive). */
  error: (path: string) => string;
  /** Record, or with undefined clear, that a field's text doesn't parse. */
  parseError: (path: string, message: string | undefined) => void;
};

export type Violation = { field: string; message: string };

export type ConfigForm = {
  view: JSX.Element;
  /**
   * Check the message as it now stands (or show the server's violations)
   * beside each field, and return whether it's valid.
   */
  validate: (server?: Violation[]) => boolean;
};

/**
 * A form for `msg` (a message of `desc`). Call it from inside a component:
 * the view's signals belong to the caller.
 */
export function createConfigForm(
  desc: DescMessage,
  msg: Message,
  opts: { onChange: () => void; choices?: Accessor<Choices>; secrets?: Secrets },
): ConfigForm {
  const [violations, setViolations] = createSignal<ReadonlyMap<string, string>>(new Map());
  const [parseErrors, setParseErrors] = createSignal<ReadonlyMap<string, string>>(new Map());
  const ctx: Ctx = {
    changed: () => opts.onChange(),
    choices: opts.choices ?? (() => new Map()),
    secrets: opts.secrets ?? { fields: new Set(), set: new Set(), clear: new Set() },
    known: new Set(),
    error: (p) => [parseErrors().get(p), violations().get(p)].filter(Boolean).join("; "),
    parseError: (p, message) =>
      setParseErrors((prev) => {
        const next = new Map(prev);
        if (message) next.set(p, message);
        else next.delete(p);
        return next;
      }),
  };
  const view = messageFields(desc, msg as unknown as Rec, "", ctx);
  const validator = createValidator();

  function validate(server?: Violation[]): boolean {
    const found =
      server ??
      (() => {
        const r = validator.validate(desc, msg as never);
        return r.kind === "valid" ? [] : r.kind === "invalid" ? r.violations.map((v) => ({ field: pathToString(v.field), message: v.message })) : [{ field: "", message: r.error.message }];
      })();
    const shown = new Map<string, string>();
    for (const v of found) {
      const path = ctx.known.has(normalizePath(v.field)) ? normalizePath(v.field) : "";
      const prev = shown.get(path);
      shown.set(path, prev ? `${prev}; ${v.message}` : v.message);
    }
    setViolations(shown);
    return found.length === 0 && parseErrors().size === 0;
  }
  return { view, validate };
}

const join = (path: string, name: string) => (path ? `${path}.${name}` : name);

/** Where a path's errors show. Registering it first keeps a nested message from showing them twice. */
function fieldError(path: string, ctx: Ctx): JSX.Element {
  ctx.known.add(path);
  return (
    <p class={ui.fieldError} role="alert">
      {ctx.error(path)}
    </p>
  );
}

function messageFields(desc: DescMessage, obj: Rec, path: string, ctx: Ctx): JSX.Element {
  const whole = ctx.known.has(path) ? null : fieldError(path, ctx);
  const seenOneofs = new Set<DescOneof>();
  const fields: JSX.Element[] = [];
  for (const f of desc.fields) {
    if (!f.oneof) fields.push(field(f, obj, path, ctx));
    else if (!seenOneofs.has(f.oneof)) {
      seenOneofs.add(f.oneof);
      fields.push(oneofField(f.oneof, obj, path, ctx));
    }
  }
  return (
    <div>
      {whole}
      {fields}
    </div>
  );
}

function field(f: DescField, obj: Rec, path: string, ctx: Ctx): JSX.Element {
  const p = join(path, f.name);
  const err = fieldError(p, ctx);
  const label = humanize(f.name);
  const key = f.localName;

  if (f.fieldKind === "map") {
    return (
      <div class={ui.field}>
        <span class={ui.label}>{label}</span>
        <p class={ui.muted}>Maps can't be edited here yet.</p>
      </div>
    );
  }
  if (f.fieldKind === "list") {
    const list = (obj[key] as unknown[]) ?? (obj[key] = []);
    // Adding or removing an item redraws the rows: each row edits list[i].
    const [rows, setRows] = createSignal(0);
    const redraw = () => {
      setRows((n) => n + 1);
      ctx.changed();
    };
    const items = () => {
      rows();
      return untrack(() =>
        list.map((_, i) => {
          const holder: Rec = { v: list[i] };
          return (
            <div class="my-2 flex items-start gap-2 [&>:first-child]:flex-1">
              {itemEditor(f, holder, "v", p, ctx, () => (list[i] = holder.v))}
              <button
                type="button"
                class={ui.btn.secondary}
                onClick={() => {
                  list.splice(i, 1);
                  redraw();
                }}
              >
                Remove
              </button>
            </div>
          );
        }),
      );
    };
    return (
      <fieldset class={ui.fieldset}>
        <legend class={ui.legend}>{label}</legend>
        {items()}
        <button
          type="button"
          class={ui.btn.secondary}
          onClick={() => {
            list.push(defaultItem(f));
            redraw();
          }}
        >
          Add {label.toLowerCase()}
        </button>
        {err}
      </fieldset>
    );
  }
  return (
    <div>
      {itemEditor(f, obj, key, p, ctx, () => {}, label)}
      {err}
    </div>
  );
}

/** One value: a scalar input, an enum select, or a nested message. */
function itemEditor(f: DescField, holder: Rec, key: string, path: string, ctx: Ctx, commit: () => void, label?: string): JSX.Element {
  const kind = f.fieldKind === "list" ? f.listKind : f.fieldKind;
  const wrap = (input: JSX.Element) =>
    label ? (
      <label class={ui.field}>
        <span class={ui.label}>{label}</span>
        {input}
      </label>
    ) : (
      input
    );
  const set = (v: unknown) => {
    holder[key] = v;
    commit();
    ctx.changed();
  };

  if (kind === "message") {
    let m = holder[key] as Rec | undefined;
    if (!m) {
      m = create(f.message!) as unknown as Rec;
      holder[key] = m;
      commit();
    }
    const inner = messageFields(f.message!, m, path, ctx);
    return label ? (
      <fieldset class={ui.fieldset}>
        <legend class={ui.legend}>{label}</legend>
        {inner}
      </fieldset>
    ) : (
      inner
    );
  }
  if (kind === "enum") {
    const e = f.enum!;
    return wrap(
      <select class={ui.select} onChange={(ev) => set(Number(ev.currentTarget.value))}>
        {e.values.map((v) => (
          <option value={String(v.number)} selected={holder[key] === v.number}>
            {v.number === 0 ? `(${enumLabel(e, v.name).toLowerCase() || "default"})` : enumLabel(e, v.name)}
          </option>
        ))}
      </select>,
    );
  }
  const t = f.scalar!;
  if (t === ScalarType.BOOL) {
    const box = <input type="checkbox" checked={holder[key] === true} onChange={(ev) => set(ev.currentTarget.checked)} />;
    return label ? (
      <label class={ui.check}>
        {box}
        <span class="text-sm font-semibold">{label}</span>
      </label>
    ) : (
      box
    );
  }
  if (t === ScalarType.BYTES) return wrap(<p class={ui.muted}>Can't be edited here.</p>);
  if (t === ScalarType.STRING && ctx.secrets.fields.has(path)) return wrap(secretInput(path, holder, key, commit, ctx));
  if (t === ScalarType.STRING) {
    // The widget's pick-list for this field, once it offers one.
    const picks = () => ctx.choices().get(path);
    return wrap(
      <Show when={picks()} keyed fallback={textInput(t, holder, key, path, ctx, commit)}>
        {(p) => choiceSelect(p, holder, key, set)}
      </Show>,
    );
  }
  return wrap(textInput(t, holder, key, path, ctx, commit));
}

function textInput(t: ScalarType, holder: Rec, key: string, path: string, ctx: Ctx, commit: () => void): JSX.Element {
  const [invalid, setInvalid] = createSignal(false);
  return (
    <input
      type="text"
      class={ui.input}
      inputmode={t === ScalarType.STRING ? "text" : "decimal"}
      autocomplete="off"
      value={holder[key] === undefined ? "" : String(holder[key])}
      aria-invalid={invalid() ? "true" : undefined}
      onInput={(ev) => {
        const r = parseScalar(t, ev.currentTarget.value);
        setInvalid(!r.ok);
        ctx.parseError(path, r.ok ? undefined : r.error);
        if (r.ok) {
          holder[key] = r.value;
          commit();
        }
        ctx.changed();
      }}
    />
  );
}

/** A oneof: pick which field is set, then edit it. */
function oneofField(o: DescOneof, obj: Rec, path: string, ctx: Ctx): JSX.Element {
  const err = fieldError(join(path, o.name), ctx);
  const current = () => obj[o.localName] as { case?: string; value?: unknown } | undefined;
  const [picked, setPicked] = createSignal(current()?.case ?? "");
  const body = () => {
    const c = picked();
    return untrack(() => {
      const f = o.fields.find((x) => x.localName === c);
      return f ? itemEditor(f, current() as Rec, "value", join(path, f.name), ctx, () => {}) : null;
    });
  };
  return (
    <fieldset class={ui.fieldset}>
      <legend class={ui.legend}>{humanize(o.name)}</legend>
      <select
        class={ui.select}
        onChange={(ev) => {
          const f = o.fields.find((x) => x.localName === ev.currentTarget.value);
          if (!f) obj[o.localName] = { case: undefined };
          else {
            const v = f.fieldKind === "message" ? create(f.message) : f.fieldKind === "enum" ? 0 : ((parseScalar(f.scalar!, "") as { value: unknown }).value ?? "");
            obj[o.localName] = { case: f.localName, value: v };
          }
          setPicked(f?.localName ?? "");
          ctx.changed();
        }}
      >
        <option value="">(none)</option>
        {o.fields.map((f) => (
          <option value={f.localName} selected={picked() === f.localName}>
            {humanize(f.name)}
          </option>
        ))}
      </select>
      {body()}
      {err}
    </fieldset>
  );
}

/**
 * A pick-list for a string field the widget offered choices for. A saved
 * value that isn't on the list stays selectable, so opening the form never
 * changes a setting by itself.
 */
function choiceSelect(picks: readonly FieldChoice[], holder: Rec, key: string, set: (v: string) => void): JSX.Element {
  const current = String(holder[key] ?? "");
  return (
    <select class={ui.select} onChange={(ev) => set(ev.currentTarget.value)}>
      <option value="">(choose)</option>
      {choiceOptions(picks, current).map((opt) => (
        <option value={opt.value} selected={opt.value === current}>
          {opt.label}
        </option>
      ))}
    </select>
  );
}

/**
 * A write-only field. It starts blank (the stored value is never sent to
 * the phone); blank keeps what's stored, typing replaces it, and "Remove"
 * deletes it.
 */
function secretInput(path: string, holder: Rec, key: string, commit: () => void, ctx: Ctx): JSX.Element {
  const stored = ctx.secrets.set.has(path);
  const [removing, setRemoving] = createSignal(false);
  let input!: HTMLInputElement;
  const field = (
    <input
      ref={input}
      type="password"
      class={ui.input}
      autocomplete="new-password"
      placeholder={stored ? "Stored. Leave blank to keep it." : "Not set"}
      disabled={removing()}
      onInput={() => {
        holder[key] = input.value;
        if (input.value) ctx.secrets.clear.delete(path);
        commit();
        ctx.changed();
      }}
    />
  );
  if (!stored) return field;
  return (
    <div class="flex flex-col gap-1">
      {field}
      <label class="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          onChange={(ev) => {
            const remove = ev.currentTarget.checked;
            if (remove) {
              ctx.secrets.clear.add(path);
              input.value = "";
              holder[key] = "";
            } else ctx.secrets.clear.delete(path);
            setRemoving(remove);
            ctx.changed();
          }}
        />
        <span>Remove</span>
      </label>
    </div>
  );
}
