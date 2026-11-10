// icon() alone: no <wd-icon>, so the element must not be bundled.
import { icon, CloudRain, Moon, Sun } from "@widgetd/sdk/icons";

const div = document.createElement("div");
div.append(icon(Sun, { size: "lg" }), icon(CloudRain, { size: 20, strokeWidth: 1.5 }));
document.body.append(div);
(globalThis as { swap?: (n: string) => void }).swap = (n) => {
  if (n === "moon") div.firstElementChild!.replaceWith(icon(Moon, { size: "lg" }));
};
