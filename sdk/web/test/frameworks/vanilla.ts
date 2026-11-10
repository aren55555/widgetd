// <wd-icon> from plain DOM.
import { defineIcons, CloudRain, Moon, Sun } from "@widgetd/sdk/icons";

defineIcons({ Sun, Moon, CloudRain });
document.body.innerHTML = `<div><wd-icon name="sun" size="lg"></wd-icon><wd-icon name="cloud-rain" size="20" stroke-width="1.5"></wd-icon></div>`;
const first = document.querySelector("wd-icon")!;
(globalThis as { swap?: (n: string) => void }).swap = (n) => first.setAttribute("name", n);
