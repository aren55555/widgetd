/** @jsxImportSource solid-js */
import "@widgetd/sdk/jsx/solid";
import { createSignal } from "solid-js";
import { render } from "solid-js/web";
import { defineIcons, CloudRain, Moon, Sun } from "@widgetd/sdk/icons";

defineIcons({ Sun, Moon, CloudRain });

const [name, setName] = createSignal("sun");
render(
  () => (
    <div>
      <wd-icon name={name()} size="lg" />
      <wd-icon name="cloud-rain" size={20} stroke-width={1.5} />
    </div>
  ),
  document.body,
);
(globalThis as { swap?: (n: string) => void }).swap = (n) => setName(n);

// Type checks (never run): the JSX types must reject these.
export const typeChecks = () => [
  // @ts-expect-error: name is required
  <wd-icon size="lg" />,
  // @ts-expect-error: not a size
  <wd-icon name="sun" size="huge" />,
  <wd-icon name="sun" size="xl" class="x" style={{ color: "red" }} onClick={() => {}} label="Sunny" />,
];
