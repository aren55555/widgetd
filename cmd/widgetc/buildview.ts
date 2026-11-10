// Builds a widget's view. widgetc embeds this and runs it with Bun:
//
//	bun buildview.ts <view dir> <out dir> <icon json> [icon name]
//
// widgetc owns the bundle's shape, which the card frame depends on: one
// classic script, view.js (a module script would need CORS in the frame's
// opaque origin), minified, with process.env.NODE_ENV set to "production" so
// libraries leave out their development builds. A widget may add Bun build
// plugins in view/plugins.ts, `export default [...]`, to compile what Bun
// doesn't by itself (Solid's JSX: @widgetd/sdk/build/solid); they change how
// the source becomes view.js, never what comes out.
//
// With an icon name (widget.toml's `icon`), it also looks the icon up in the
// view's own @widgetd/sdk/icons, the same way <wd-icon> does, and writes its
// shapes as JSON for the package manifest.

import type { BunPlugin } from "bun";
import { existsSync } from "node:fs";
import { join, resolve } from "node:path";

const [viewArg, outArg, iconOut, iconName] = process.argv.slice(2);
if (!viewArg || !outArg || !iconOut) fail("usage: bun buildview.ts <view dir> <out dir> <icon json> [icon name]");
const viewDir = resolve(viewArg);
const outDir = resolve(outArg);

function fail(msg: string): never {
  console.error(msg);
  process.exit(1);
}

let plugins: BunPlugin[] = [];
const hook = join(viewDir, "plugins.ts");
if (existsSync(hook)) {
  const exported = (await import(hook)).default;
  if (!Array.isArray(exported)) fail("view/plugins.ts: export default an array of Bun plugins");
  plugins = exported;
}

const result = await Bun.build({
  entrypoints: [join(viewDir, "view.ts")],
  outdir: outDir,
  naming: "view.js",
  format: "iife",
  target: "browser",
  minify: true,
  define: { "process.env.NODE_ENV": '"production"' },
  plugins,
});
if (!result.success) {
  for (const log of result.logs) console.error(log);
  fail("view: build failed");
}
// The frame loads view.js and view.css and nothing else, so anything else the
// build emits (an imported image, a font) would be missing at run time.
const extra = result.outputs.map((o) => o.path).filter((p) => p !== join(outDir, "view.js"));
if (extra.length) {
  fail(
    `view: the build emitted ${extra.map((p) => p.slice(outDir.length + 1)).join(", ")}, which a card can't load.\n` +
      `A card loads only view.js and view.css: inline images and fonts as data: URIs, or draw icons with @widgetd/sdk/icons.`,
  );
}
const js = result.outputs[0]!;
console.log(`  view.js  ${(js.size / 1024).toFixed(1)} KB`);

if (iconName) {
  // Only after bundling: once this process has imported lucide, Bun.build
  // in it no longer tree-shakes lucide (seen with Bun 1.4.0).
  let icons: Record<string, unknown> & { IconRegistry: new () => { add(i: Record<string, unknown>): void; get(n: string): unknown } };
  try {
    icons = await import(Bun.resolveSync("@widgetd/sdk/icons", viewDir));
  } catch (e) {
    fail(`widget.toml: icon = "${iconName}" needs @widgetd/sdk (with /icons) among the view's dependencies: ${(e as Error).message}`);
  }
  const registry = new icons.IconRegistry();
  registry.add(icons);
  const node = registry.get(iconName) as [string, Record<string, string | number>][] | undefined;
  if (!node) fail(`widget.toml: icon "${iconName}" isn't a Lucide icon; see https://lucide.dev/icons for names`);
  const shapes = node.map(([tag, attrs]) => ({
    tag,
    attrs: Object.fromEntries(Object.entries(attrs).map(([k, v]) => [k, String(v)])),
  }));
  await Bun.write(iconOut, JSON.stringify({ name: iconName, shapes }));
}
