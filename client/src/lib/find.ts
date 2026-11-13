// Finding an instance or a screen on the admin page by what a screen shows:
// a card's short id (from its set-up code), the screen's own (from its
// footer), or a name.

import type { InstanceInfo } from "../gen/widgetd/v1/admin_pb.ts";
import type { Device } from "../gen/widgetd/v1/auth_pb.ts";

/** A search as typed: trimmed, lower case, without a leading "#". */
export const normalizeQuery = (q: string) => q.trim().toLowerCase().replace(/^#/, "");

/** Whether a card's short id starts with the (normalized) query. */
export const shortIdMatches = (shortId: string, query: string) => query !== "" && shortId.startsWith(query);

/**
 * Whether an instance answers the (normalized) query: one of its cards'
 * short ids starts with it, or its id, label or widget name contains it.
 * An empty query matches everything.
 */
export function instanceMatches(inst: InstanceInfo, title: string, query: string): boolean {
  if (!query) return true;
  if (inst.cards.some((c) => shortIdMatches(c.shortId, query))) return true;
  return [inst.id, inst.label, inst.widget, title].some((s) => s.toLowerCase().includes(query));
}

/**
 * Whether a screen answers the (normalized) query: its short id starts
 * with it, or its name contains it. An empty query matches everything.
 */
export const deviceMatches = (d: Device, query: string) =>
  !query || shortIdMatches(d.shortId, query) || d.name.toLowerCase().includes(query);
