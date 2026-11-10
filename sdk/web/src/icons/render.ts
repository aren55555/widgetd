// Drawing a Lucide icon as an <svg>, for any view. No side effects at import:
// a view that only calls icon() bundles just this and the icons it names.

import { createElement, type IconNode } from "lucide";

export type { IconNode } from "lucide";

/** The named size steps, in CSS pixels. */
export const ICON_SIZES = { sm: 16, md: 24, lg: 32, xl: 48 } as const;

/** A named step, or a size in pixels. */
export type IconSize = keyof typeof ICON_SIZES | number | `${number}`;

export type IconOptions = {
  /** Default "md". */
  size?: IconSize;
  /** Overrides the board theme's `--wd-icon-stroke` for this icon. */
  strokeWidth?: number | string;
  /** Names the icon for screen readers; without it the icon is decorative. */
  label?: string;
};

/** Pixels for a size (or an attribute's text), or undefined if it is neither a step nor a positive number. */
export function iconPixels(size: IconSize | (string & {}) | null | undefined): number | undefined {
  if (size == null || size === "") return ICON_SIZES.md;
  if (typeof size === "string" && size in ICON_SIZES) return ICON_SIZES[size as keyof typeof ICON_SIZES];
  const n = Number(size);
  return Number.isFinite(n) && n > 0 ? n : undefined;
}

/**
 * Finds icons by Lucide's name ("cloud-sun") among icons registered under
 * their export names (CloudSun). Lucide's names can't all be derived from
 * export names (ArrowDown01 is arrow-down-0-1, but Grid2x2 is grid-2x2), so a
 * name matches in two steps: exactly, against each export name in kebab case;
 * failing that, ignoring case and dashes, if that picks out one icon. (Both
 * steps are needed: letter-s and letters differ only by a dash.)
 */
export class IconRegistry {
  private exact = new Map<string, IconNode>();
  private loose = new Map<string, Set<IconNode>>();

  /** Register icons keyed by their export names. */
  add(icons: Record<string, unknown>): void {
    for (const [exportName, node] of Object.entries(icons)) {
      if (!isIconNode(node)) continue;
      this.exact.set(kebab(exportName), node);
      const k = looseKey(exportName);
      const set = this.loose.get(k) ?? new Set();
      set.add(node);
      this.loose.set(k, set);
    }
  }

  /** The icon Lucide calls `name`, if registered and unambiguous. */
  get(name: string): IconNode | undefined {
    const hit = this.exact.get(name);
    if (hit) return hit;
    const candidates = this.loose.get(looseKey(name));
    return candidates?.size === 1 ? [...candidates][0] : undefined;
  }
}

const kebab = (exportName: string) =>
  exportName
    .replace(/([a-z])([A-Z0-9])/g, "$1-$2")
    .replace(/([A-Z])([A-Z][a-z])/g, "$1-$2")
    .replace(/([0-9])([A-Z])/g, "$1-$2")
    .toLowerCase();
const looseKey = (name: string) => name.toLowerCase().replace(/[^a-z0-9]/g, "");

/** Whether `v` looks like a Lucide icon: a list of [tag, attributes]. */
export function isIconNode(v: unknown): v is IconNode {
  return Array.isArray(v) && v.length > 0 && v.every((s) => Array.isArray(s) && typeof s[0] === "string" && typeof s[1] === "object");
}

// The theme's stroke applies only where an icon doesn't set its own, and at
// zero specificity, so any rule in a widget's CSS overrides it. <wd-icon>
// lays out like its icon rather than like a line of text.
const THEME_CSS =
  ":where(svg.wd-icon:not([data-wd-stroke])){stroke-width:var(--wd-icon-stroke,2)}" +
  ":where(wd-icon){display:inline-flex;vertical-align:middle;line-height:0}";
let styled: Document | undefined;
function themeStroke(doc: Document): void {
  if (styled === doc) return;
  styled = doc;
  const style = doc.createElement("style");
  style.dataset.wdIcons = "";
  style.textContent = THEME_CSS;
  doc.head.append(style);
}

/**
 * An <svg> of `node`, sized from the scale and stroked per the board theme.
 * Its color is `currentColor`: set `color` on it or anything around it.
 */
export function icon(node: IconNode, opts: IconOptions = {}): SVGSVGElement {
  themeStroke(document);
  const px = iconPixels(opts.size) ?? ICON_SIZES.md;
  const svg = createElement(node, { width: px, height: px, class: "wd-icon" }) as SVGSVGElement;
  if (opts.strokeWidth != null && opts.strokeWidth !== "") {
    svg.setAttribute("stroke-width", String(opts.strokeWidth));
    svg.setAttribute("data-wd-stroke", "");
  }
  if (opts.label) {
    svg.setAttribute("role", "img");
    svg.setAttribute("aria-label", opts.label);
  } else {
    svg.setAttribute("aria-hidden", "true");
  }
  return svg;
}
