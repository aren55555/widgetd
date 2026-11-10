// Types <wd-icon> for Preact's JSX. Import once anywhere in a view:
//
//	import "@widgetd/sdk/jsx/preact";
//
// It takes what any element takes (class, style, events…), plus its own
// attributes.

import type { JSX as PreactJSX } from "preact";
import type { WdIconAttributes } from "../icons/element.ts";

declare module "preact" {
  namespace JSX {
    interface IntrinsicElements {
      "wd-icon": Omit<PreactJSX.IntrinsicElements["span"], keyof WdIconAttributes> & WdIconAttributes;
    }
  }
}

export {};
