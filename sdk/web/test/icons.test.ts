// @widgetd/sdk/icons: the name mapping against all of Lucide, and <wd-icon>
// in every officially supported framework, each bundled the way widgetc
// bundles a view and run in a fresh DOM.

import { describe, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { dirname, join } from "node:path";
import { gzipSync } from "node:zlib";
import * as Lucide from "lucide";
import { Bitcoin, CloudRain, Moon, Sun, type IconNode } from "lucide";
import { IconRegistry, iconPixels } from "../src/icons/render.ts";

const lucideDir = dirname(Bun.resolveSync("lucide/package.json", import.meta.dir));

describe("IconRegistry", () => {
  // lucide.mjs re-exports each icon file under all its names:
  //   export { default as Grid2X2Check, default as Grid2x2Check } from './icons/grid-2x2-check.mjs';
  // The file name is Lucide's name for the icon.
  const lucideIcons = async () => {
    const src = await Bun.file(join(lucideDir, "dist/esm/lucide.mjs")).text();
    const all = Lucide as unknown as Record<string, IconNode>;
    return [...src.matchAll(/export \{ ([^}]+) \} from '\.\/icons\/([a-z0-9-]+)\.mjs'/g)].map(([, names, file]) => {
      const exports = names!.split(",").map((n) => n.trim().replace(/^default as /, ""));
      return { file: file!, exports, node: all[exports[0]!]! };
    });
  };

  test("with all of Lucide registered, finds every icon by its Lucide name", async () => {
    const icons = await lucideIcons();
    expect(icons.length).toBeGreaterThan(1500);
    const r = new IconRegistry();
    r.add(Lucide);
    expect(icons.filter(({ file, node }) => r.get(file) !== node).map((i) => i.file)).toEqual([]);
  });

  test("finds an icon registered alone under its own export name", async () => {
    // Its own name: not a legacy alias like User2 for user-round.
    const loose = (s: string) => s.toLowerCase().replace(/[^a-z0-9]/g, "");
    const missed = (await lucideIcons()).filter(({ file, exports, node }) =>
      exports
        .filter((e) => loose(e) === loose(file))
        .some((e) => {
          const r = new IconRegistry();
          r.add({ [e]: node });
          return r.get(file) !== node;
        }),
    );
    expect(missed.map((i) => i.file)).toEqual([]);
  });

  test("irregular names, and names a dash apart", () => {
    const r = new IconRegistry();
    r.add({ ArrowDown01: Sun, Grid2x2Check: Moon, LetterS: CloudRain, Letters: Bitcoin });
    expect(r.get("arrow-down-0-1")).toBe(Sun);
    expect(r.get("grid-2x2-check")).toBe(Moon);
    expect(r.get("letter-s")).toBe(CloudRain);
    expect(r.get("letters")).toBe(Bitcoin);
    expect(r.get("nope")).toBeUndefined();
  });
});

test("iconPixels", () => {
  expect(iconPixels(undefined)).toBe(24);
  expect(iconPixels("sm")).toBe(16);
  expect(iconPixels("xl")).toBe(48);
  expect(iconPixels(20)).toBe(20);
  expect(iconPixels("56")).toBe(56);
  expect(iconPixels("huge" as never)).toBeUndefined();
  expect(iconPixels(-1)).toBeUndefined();
});

// --- bundled views ---------------------------------------------------------

const bundles = new Map<string, string>();
async function bundle(fixture: string): Promise<string> {
  const hit = bundles.get(fixture);
  if (hit) return hit;
  // In a separate process: see frameworks/bundle.ts.
  const p = Bun.spawn([process.execPath, join(import.meta.dir, "frameworks", "bundle.ts"), fixture], { stderr: "pipe" });
  const [code, err, exit] = await Promise.all([new Response(p.stdout).text(), new Response(p.stderr).text(), p.exited]);
  if (exit !== 0) throw new Error(`bundling ${fixture}: ${err}`);
  bundles.set(fixture, code);
  return code;
}

type Page = { w: Window; warnings: string[]; svgs: () => SVGSVGElement[] };
async function run(fixture: string): Promise<Page> {
  const w = new Window({ settings: { enableJavaScriptEvaluation: true, suppressInsecureJavaScriptEnvironmentWarning: true } as never });
  const warnings: string[] = [];
  w.console.warn = (...a: unknown[]) => void warnings.push(a.join(" "));
  w.console.error = (...a: unknown[]) => void warnings.push(a.join(" "));
  w.eval(await bundle(fixture));
  await w.happyDOM.waitUntilComplete();
  return { w, warnings, svgs: () => [...w.document.querySelectorAll("svg")] as unknown as SVGSVGElement[] };
}

/** An <svg>'s shapes, comparable with an IconNode's. */
const shapes = (svg: Element) =>
  [...svg.children].map((c) => c.tagName.toLowerCase() + ":" + [...c.attributes].map((a) => `${a.name}=${a.value}`).sort().join(",")).join("|");
const nodeShapes = (n: IconNode) =>
  n.map(([tag, attrs]) => tag + ":" + Object.entries(attrs).map(([k, v]) => `${k}=${v}`).sort().join(",")).join("|");

const frameworks = ["vanilla.ts", "vanilla-fn.ts", "preact.tsx", "react.tsx", "solid.tsx", "lit.ts"];

describe.each(frameworks)("%s", (fixture) => {
  test("draws, sizes and strokes each icon", async () => {
    const { svgs, warnings, w } = await run(fixture);
    const [first, second] = svgs();
    expect(svgs()).toHaveLength(2);
    expect(shapes(first!)).toBe(nodeShapes(Sun));
    expect(first!.getAttribute("width")).toBe("32");
    expect(first!.getAttribute("class")).toBe("wd-icon");
    expect(first!.getAttribute("aria-hidden")).toBe("true");
    expect(first!.hasAttribute("data-wd-stroke")).toBe(false);
    expect(shapes(second!)).toBe(nodeShapes(CloudRain));
    expect(second!.getAttribute("width")).toBe("20");
    expect(second!.getAttribute("stroke-width")).toBe("1.5");
    expect(second!.hasAttribute("data-wd-stroke")).toBe(true);
    // The theme stroke rule, once.
    expect(w.document.querySelectorAll("style[data-wd-icons]")).toHaveLength(1);
    expect(warnings).toEqual([]);
  });

  test("redraws when the view's state changes the icon", async () => {
    const { svgs, w } = await run(fixture);
    (w as unknown as { swap: (n: string) => void }).swap("moon");
    await w.happyDOM.waitUntilComplete();
    expect(shapes(svgs()[0]!)).toBe(nodeShapes(Moon));
    expect(svgs()[0]!.getAttribute("width")).toBe("32");
  });

  test("bundles only the icons it uses", async () => {
    const code = await bundle(fixture);
    expect(code.includes(Bitcoin[0]![1].d as string)).toBe(false);
  });
});

test("icon() alone doesn't bundle <wd-icon>", async () => {
  expect((await bundle("vanilla-fn.ts")).includes("customElements")).toBe(false);
  expect((await bundle("vanilla.ts")).includes("customElements")).toBe(true);
});

test("the icon code stays small", async () => {
  // Three icons, the renderer, the element and the registry, gzipped.
  const gz = gzipSync(await bundle("vanilla.ts")).length;
  expect(gz).toBeLessThan(2500);
});

describe("<wd-icon>", () => {
  type Api = { defineIcons: (i: Record<string, IconNode>) => void; icon: unknown; icons: Record<string, IconNode> };
  const page = async () => {
    const p = await run("api.ts");
    const api = (p.w as unknown as { api: Api }).api;
    const el = (html: string) => {
      p.w.document.body.innerHTML = html;
      return p.w.document.body.firstElementChild as unknown as HTMLElement & Record<string, unknown>;
    };
    return { ...p, api, el };
  };

  test("elements in the page before defineIcons draw once it runs, without warnings", async () => {
    const { api, el, svgs, warnings } = await page();
    el(`<wd-icon name="sun"></wd-icon>`);
    expect(svgs()).toHaveLength(0);
    api.defineIcons({ Sun: api.icons.Sun! });
    expect(svgs()).toHaveLength(1);
    expect(warnings).toEqual([]);
  });

  test("an unregistered name draws nothing and warns once", async () => {
    const { api, el, svgs, warnings } = await page();
    api.defineIcons({ Sun: api.icons.Sun! });
    el(`<div><wd-icon name="moon"></wd-icon><wd-icon name="moon"></wd-icon></div>`);
    expect(svgs()).toHaveLength(0);
    expect(warnings).toHaveLength(1);
    expect(warnings[0]).toContain("defineIcons({ Moon })");
    // Registering it later fills them in.
    api.defineIcons({ Moon: api.icons.Moon! });
    expect(svgs()).toHaveLength(2);
  });

  test("properties reflect to attributes", async () => {
    const { api, el, svgs } = await page();
    api.defineIcons({ Sun: api.icons.Sun!, Moon: api.icons.Moon! });
    const e = el(`<wd-icon></wd-icon>`);
    e.name = "moon";
    e.size = "xl";
    e["stroke-width"] = 1;
    expect(e.getAttribute("name")).toBe("moon");
    expect(svgs()[0]!.getAttribute("width")).toBe("48");
    expect(svgs()[0]!.getAttribute("stroke-width")).toBe("1");
    e.strokeWidth = null;
    expect(svgs()[0]!.hasAttribute("stroke-width")).toBe(true); // Lucide's own default
    expect(svgs()[0]!.hasAttribute("data-wd-stroke")).toBe(false);
  });

  test("a label makes it an image for screen readers", async () => {
    const { api, el, svgs } = await page();
    api.defineIcons({ Sun: api.icons.Sun! });
    el(`<wd-icon name="sun" label="Sunny"></wd-icon>`);
    expect(svgs()[0]!.getAttribute("role")).toBe("img");
    expect(svgs()[0]!.getAttribute("aria-label")).toBe("Sunny");
    expect(svgs()[0]!.hasAttribute("aria-hidden")).toBe(false);
  });

  test("an unknown size draws nothing", async () => {
    const { api, el, svgs } = await page();
    api.defineIcons({ Sun: api.icons.Sun! });
    el(`<wd-icon name="sun" size="huge"></wd-icon>`);
    expect(svgs()).toHaveLength(0);
  });
});
