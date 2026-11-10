// Exposes the API to the test, which drives the element directly.
import { defineIcons, icon, Bitcoin, Moon, Sun } from "@widgetd/sdk/icons";

(globalThis as Record<string, unknown>).api = { defineIcons, icon, icons: { Sun, Moon, Bitcoin } };
