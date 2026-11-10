// Types <wd-icon> for Solid's JSX. Import once anywhere in a view:
//
//	import "@widgetd/sdk/jsx/solid";
//
// It takes what any element takes (class, style, events…), plus its own
// attributes. Solid sets a custom element's props as properties; <wd-icon>
// reflects each one to its attribute, so `name={n}` works as in any other
// framework.

import type { JSX as SolidJSX } from "solid-js";
import type { WdIconAttributes } from "../icons/element.ts";

declare module "solid-js" {
  namespace JSX {
    interface IntrinsicElements {
      "wd-icon": Omit<SolidJSX.IntrinsicElements["span"], keyof WdIconAttributes> & WdIconAttributes;
    }
  }
}

export {};
