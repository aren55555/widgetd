// Types <wd-icon> for React's JSX (React 19). Import once anywhere in a view:
//
//	import "@widgetd/sdk/jsx/react";
//
// It takes what any element takes (className, style, events…), plus its own
// attributes.

import type { JSX as ReactJSX } from "react";
import type { WdIconAttributes } from "../icons/element.ts";

declare module "react" {
  namespace JSX {
    interface IntrinsicElements {
      "wd-icon": Omit<ReactJSX.IntrinsicElements["span"], keyof WdIconAttributes> & WdIconAttributes;
    }
  }
}

export {};
