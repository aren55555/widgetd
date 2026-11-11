// A widget's own icon (its widget.toml `icon`), drawn on widgetd's pages: the
// board's card headers and the admin pages. These pages aren't sandboxed, so
// the icon arrives as data (widgetd.v1.Icon) and is built here element by
// element with createElementNS, never parsed as markup. The host already
// refuses anything but Lucide's shapes and geometry; this checks again, and
// draws nothing rather than anything unexpected.

import type { Icon } from "../gen/widgetd/v1/stream_pb.ts";

const SVG = "http://www.w3.org/2000/svg";

/** The attributes each shape may have: what Lucide's icons use. Mirrors host/icon.go. */
const SHAPES: Partial<Record<string, ReadonlySet<string>>> = {
  path: new Set(["d"]),
  circle: new Set(["cx", "cy", "r", "fill"]),
  rect: new Set(["x", "y", "width", "height", "rx", "ry"]),
  line: new Set(["x1", "y1", "x2", "y2"]),
  ellipse: new Set(["cx", "cy", "rx", "ry"]),
  polyline: new Set(["points"]),
  polygon: new Set(["points"]),
};
const GEOMETRY = /^[0-9MmLlHhVvCcSsQqTtAaZz ,.eE+-]*$/;

/**
 * An <svg> of a widget's icon, `size` pixels square, stroked in currentColor;
 * undefined if there is no icon or it isn't plain Lucide geometry.
 * Decorative: the title beside it says what it is.
 */
export function widgetIcon(icon: Icon | undefined, size = 16, doc: Document = document): SVGSVGElement | undefined {
  if (!icon || icon.shapes.length === 0) return undefined;
  const svg = doc.createElementNS(SVG, "svg");
  for (const [k, v] of Object.entries({
    viewBox: "0 0 24 24",
    width: String(size),
    height: String(size),
    fill: "none",
    stroke: "currentColor",
    "stroke-width": "2",
    "stroke-linecap": "round",
    "stroke-linejoin": "round",
    "aria-hidden": "true",
    class: "widget-icon",
  })) {
    svg.setAttribute(k, v);
  }
  for (const shape of icon.shapes) {
    const allowed = SHAPES[shape.tag];
    if (!allowed) return undefined;
    const el = doc.createElementNS(SVG, shape.tag);
    for (const [k, v] of Object.entries(shape.attrs)) {
      const ok = k === "fill" ? v === "none" || v === "currentColor" : allowed.has(k) && GEOMETRY.test(v);
      if (!ok || !allowed.has(k)) return undefined;
      el.setAttribute(k, v);
    }
    svg.append(el);
  }
  return svg;
}

/** CSS for widgetIcon's <svg>: its stroke follows the theme's icon stroke. */
export const WIDGET_ICON_CSS = `.widget-icon { flex: none; stroke-width: var(--wd-icon-stroke, 2); }`;
