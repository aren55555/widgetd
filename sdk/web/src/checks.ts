// How a view confirms that what arrived over the shared pipe is what its
// schema says: the right type, bytes that decode as it, no fields the schema
// doesn't define, and every protovalidate rule satisfied. The server ran the
// same checks before sending; running them again here means a view never
// has to trust anything between the widget's server half and itself.

import { fromBinary, type DescMessage, type Message, type MessageShape } from "@bufbuild/protobuf";
import { createValidator, type Validator } from "@bufbuild/protovalidate";

export const typeUrlOf = (schema: DescMessage): string => `type.googleapis.com/${schema.typeName}`;

export type Checked<T> = { ok: true; message: T } | { ok: false; error: string };

let shared: Validator | undefined;
const validator = (): Validator => (shared ??= createValidator());

/** True if `m`, or any message inside it, carries fields its schema lacks. */
export function hasUnknown(m: Message): boolean {
  if (m.$unknown?.length) return true;
  for (const v of Object.values(m)) {
    if (Array.isArray(v)) {
      if (v.some((x) => isMessage(x) && hasUnknown(x))) return true;
    } else if (isMessage(v)) {
      if (hasUnknown(v)) return true;
    } else if (v && typeof v === "object" && "case" in v && isMessage((v as { value?: unknown }).value)) {
      if (hasUnknown((v as { value: Message }).value)) return true;
    } else if (typeof v === "object" && !ArrayBuffer.isView(v)) {
      // map fields are plain objects
      for (const mv of Object.values(v)) if (isMessage(mv) && hasUnknown(mv)) return true;
    }
  }
  return false;
}

const isMessage = (v: unknown): v is Message =>
  typeof v === "object" && v !== null && typeof (v as { $typeName?: unknown }).$typeName === "string";

/** Decode and check one payload. */
export function decodeChecked<Desc extends DescMessage>(
  schema: Desc,
  typeUrl: string,
  value: Uint8Array,
): Checked<MessageShape<Desc>> {
  if (typeUrl !== typeUrlOf(schema)) {
    return { ok: false, error: `expected ${schema.typeName}, got ${typeUrl || "no type"}` };
  }
  let message: MessageShape<Desc>;
  try {
    message = fromBinary(schema, value);
  } catch (e) {
    return { ok: false, error: `${schema.typeName} does not decode: ${(e as Error).message}` };
  }
  if (hasUnknown(message)) {
    return { ok: false, error: `${schema.typeName} carries fields its schema doesn't define` };
  }
  return validated(schema, message);
}

/** Check protovalidate rules on a message already in hand. */
export function validated<Desc extends DescMessage>(
  schema: Desc,
  message: MessageShape<Desc>,
): Checked<MessageShape<Desc>> {
  const r = validator().validate(schema, message);
  if (r.kind === "valid") return { ok: true, message };
  return { ok: false, error: `${schema.typeName}: ${r.error.message}` };
}
