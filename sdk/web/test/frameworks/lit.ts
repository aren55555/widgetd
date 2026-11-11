import { html, render } from "lit";
import { defineIcons, CloudRain, Moon, Sun } from "@widgetd/sdk/icons";

defineIcons({ Sun, Moon, CloudRain });

const draw = (name: string) =>
  render(
    html`<div>
      <wd-icon name=${name} size="lg"></wd-icon><wd-icon name="cloud-rain" size="20" stroke-width="1.5"></wd-icon>
    </div>`,
    document.body,
  );
draw("sun");
(globalThis as { swap?: (n: string) => void }).swap = draw;
