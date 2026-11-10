/** @jsxImportSource react */
import "@widgetd/sdk/jsx/react";
import { useState } from "react";
import { flushSync } from "react-dom";
import { createRoot } from "react-dom/client";
import { defineIcons, CloudRain, Moon, Sun } from "@widgetd/sdk/icons";

defineIcons({ Sun, Moon, CloudRain });

let set: (n: string) => void = () => {};
function App() {
  const [name, setName] = useState("sun");
  set = setName;
  return (
    <div>
      <wd-icon name={name} size="lg" />
      <wd-icon name="cloud-rain" size={20} stroke-width={1.5} />
    </div>
  );
}
// Type checks (never run): the JSX types must reject these.
export const typeChecks = () => [
  // @ts-expect-error: name is required
  <wd-icon size="lg" />,
  // @ts-expect-error: not a size
  <wd-icon name="sun" size="huge" />,
  <wd-icon name="sun" size="xl" className="x" style={{ color: "red" }} onClick={() => {}} label="Sunny" />,
];

const root = createRoot(document.body);
flushSync(() => root.render(<App />));
(globalThis as { swap?: (n: string) => void }).swap = (n) => flushSync(() => set(n));
