// <wd-icon>: one way to draw an icon in any framework. Custom elements are
// part of the platform, so Preact, React, Solid, Lit, Vue and plain DOM all
// render the same tag:
//
//	<wd-icon name="cloud-sun" size="lg"></wd-icon>
//
// Icons are found by name in a registry the view fills with defineIcons, so
// the bundle holds only the icons the view registers. The element is created
// and defined by the first defineIcons call, not on import: a view that never
// calls it doesn't bundle it, importing this module needs no DOM (scripts,
// unit tests), and elements already in the page upgrade after their icons
// are registered.

import { icon, IconRegistry, iconPixels, type IconNode, type IconSize } from "./render.ts";

const TAG = "wd-icon";
const registry = new IconRegistry();
const warned = new Set<string>();

/** The attributes of <wd-icon>; each is also a property. */
export interface WdIconAttributes {
  /** Lucide's name for the icon, e.g. "cloud-sun". Register it first with defineIcons. */
  name: string;
  /** "sm" | "md" | "lg" | "xl" (16/24/32/48px), or pixels. Default "md". */
  size?: IconSize;
  /** Overrides the theme's `--wd-icon-stroke`. */
  "stroke-width"?: number | `${number}`;
  /** Names the icon for screen readers; without it the icon is decorative. */
  label?: string;
}

/** A <wd-icon> element. */
export interface WdIconElement extends HTMLElement {
  name: string;
  size: string | null;
  strokeWidth: string | null;
  label: string | null;
  /** Draw the registered icon again. */
  render(): void;
}

/**
 * Register the icons a view draws with <wd-icon>, keyed by their Lucide
 * export names: `defineIcons({ Sun, CloudRain })` makes "sun" and
 * "cloud-rain" available. Defines the element on first call.
 */
export function defineIcons(icons: Record<string, IconNode>): void {
  registry.add(icons);
  if (!customElements.get(TAG)) {
    customElements.define(TAG, elementClass());
    return; // defining upgrades, and so renders, every element already there
  }
  // An element defined by another copy of the SDK may lack render().
  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
  for (const el of document.querySelectorAll(TAG)) el.render?.();
}

function elementClass(): CustomElementConstructor {
  // Frameworks differ in whether they set attributes or properties on custom
  // elements (Solid sets properties; React 19 and Vue sometimes do), so every
  // attribute is also a property that reflects to it.
  const reflect = (el: HTMLElement, attr: string, v: string | number | null) => {
    if (v == null || v === "") el.removeAttribute(attr);
    else el.setAttribute(attr, String(v));
  };
  return class extends HTMLElement implements WdIconElement {
    static observedAttributes = ["name", "size", "stroke-width", "label"];

    get name(): string {
      return this.getAttribute("name") ?? "";
    }
    set name(v: string | null) {
      reflect(this, "name", v);
    }
    get size(): string | null {
      return this.getAttribute("size");
    }
    set size(v: string | null) {
      reflect(this, "size", v);
    }
    get strokeWidth(): string | null {
      return this.getAttribute("stroke-width");
    }
    set strokeWidth(v: number | string | null) {
      reflect(this, "stroke-width", v);
    }
    get ["stroke-width"](): string | null {
      return this.strokeWidth;
    }
    set ["stroke-width"](v: number | string | null) {
      this.strokeWidth = v;
    }
    get label(): string | null {
      return this.getAttribute("label");
    }
    set label(v: string | null) {
      reflect(this, "label", v);
    }

    connectedCallback(): void {
      this.render();
    }

    attributeChangedCallback(): void {
      if (this.isConnected) this.render();
    }

    render(): void {
      const name = this.name;
      const node = registry.get(name);
      const px = iconPixels(this.size);
      if (!node || px === undefined) {
        this.replaceChildren();
        if (name && !node && !warned.has(name)) {
          warned.add(name);
          console.warn(
            `<wd-icon>: no icon "${name}"; register it with defineIcons, e.g. defineIcons({ ${pascal(name)} })`,
          );
        }
        return;
      }
      this.replaceChildren(
        icon(node, { size: px, strokeWidth: this.strokeWidth ?? undefined, label: this.label ?? undefined }),
      );
    }
  };
}

const pascal = (name: string) => name.replace(/(^|-)([a-z0-9])/g, (_m, _d, c: string) => c.toUpperCase());

declare global {
  interface HTMLElementTagNameMap {
    "wd-icon": WdIconElement;
  }
}
