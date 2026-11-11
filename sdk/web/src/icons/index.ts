// @widgetd/sdk/icons: Lucide's icons at the version widgetd pins, and two
// ways to draw them in a card.
//
//	import { defineIcons, icon, Sun, CloudRain } from "@widgetd/sdk/icons";
//
//	root.append(icon(Sun, { size: "lg" }));          // an <svg>, plain DOM
//
//	defineIcons({ Sun, CloudRain });                 // any framework:
//	<wd-icon name="cloud-rain" size="lg"></wd-icon>
//
// Each icon is plain data, so the bundle holds only the icons a view imports.
// Icons are 24×24 outlines colored with currentColor; their stroke follows
// the board theme's --wd-icon-stroke unless set per icon.

export * from "lucide";
export {
  icon,
  iconPixels,
  IconRegistry,
  isIconNode,
  ICON_SIZES,
  type IconNode,
  type IconOptions,
  type IconSize,
} from "./render.ts";
export { defineIcons, type WdIconAttributes, type WdIconElement } from "./element.ts";
