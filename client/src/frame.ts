// The card frame's bootstrap (see lib/frameboot.ts). Built to dist/frame.js,
// which the host inlines into every card frame ahead of the widget's code.

import { reportUncaught, takeConsole } from "./lib/frameboot.ts";

const parent = window.parent;
const send = (m: Parameters<Parameters<typeof takeConsole>[2]>[0]) => {
  parent.postMessage(m, "*");
};
reportUncaught(window, send);
takeConsole(console, window, send);
