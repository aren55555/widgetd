// Builds a widget's view. widgetc embeds this and runs it with Bun:
//
//	bun buildview.ts <view dir> <out dir> <request json> <extras json>
//
// The request is {icon, fonts} from widget.toml; the extras written back are
// what they resolved to: {icon, fonts, fontFaces}.
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
// view's own @widgetd/sdk/icons, the same way <wd-icon> does, and returns its
// shapes for the package manifest.
//
// With fonts (widget.toml's [[fonts]]), it finds each family's Fontsource
// package among the view's own dependencies, the way it finds the icons, and
// copies the faces asked for (.woff2 only: every browser a board runs reads
// it) and the font's licence into <out dir>/fonts/. It returns their
// @font-face rules, for widgetc to put ahead of view.css, whose URLs are
// relative to view.css and so resolve beside it.

import type { BunPlugin } from "bun";
import { copyFileSync, existsSync, mkdirSync, readFileSync } from "node:fs";
import { basename, dirname, join, resolve } from "node:path";

interface FontRequest {
  family: string;
  weights: number[];
  styles: Array<"normal" | "italic">;
  subsets: string[];
}
interface Request {
  icon: string;
  fonts: FontRequest[] | null;
}

const [viewArg, outArg, requestPath, extrasPath] = process.argv.slice(2);
if (!viewArg || !outArg || !requestPath || !extrasPath)
  fail("usage: bun buildview.ts <view dir> <out dir> <request json> <extras json>");
const viewDir = resolve(viewArg);
const outDir = resolve(outArg);
const request = JSON.parse(readFileSync(requestPath, "utf8")) as Request;

function fail(msg: string): never {
  console.error(msg);
  process.exit(1);
}

let plugins: BunPlugin[] = [];
const hook = join(viewDir, "plugins.ts");
if (existsSync(hook)) {
  const exported = ((await import(hook)) as { default?: unknown }).default;
  if (!Array.isArray(exported)) fail("view/plugins.ts: export default an array of Bun plugins");
  plugins = exported as BunPlugin[];
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
      `A card loads only view.js and view.css: inline images as data: URIs, draw icons with @widgetd/sdk/icons, ` +
      `and declare fonts in widget.toml's [[fonts]].`,
  );
}
const js = result.outputs[0];
console.log(`  view.js  ${(js.size / 1024).toFixed(1)} KB`);

const extras: { icon?: unknown; fonts?: unknown[]; fontFaces?: string } = {};

for (const font of request.fonts ?? []) {
  const id = font.family.toLowerCase().replaceAll(" ", "-");
  const pkg = `@fontsource/${id}`;
  let license: string;
  try {
    // Fontsource exports LICENSE but not package.json: it locates the package.
    license = Bun.resolveSync(`${pkg}/LICENSE`, viewDir);
  } catch {
    fail(`widget.toml: font "${font.family}" needs ${pkg} among the view's dependencies (bun add ${pkg} in view/)`);
  }
  const dir = dirname(license);
  const { version } = JSON.parse(readFileSync(join(dir, "package.json"), "utf8")) as { version: string };
  mkdirSync(join(outDir, "fonts"), { recursive: true });
  const licenseOut = `fonts/${id}-LICENSE.txt`;
  copyFileSync(license, join(outDir, licenseOut));
  const files: string[] = [];
  let css = `/* ${font.family}, from ${pkg}@${version}; licence: ${licenseOut} */\n`;
  for (const subset of font.subsets)
    for (const weight of font.weights)
      for (const style of font.styles) {
        // e.g. latin-700.css, latin-700-italic.css: one face, naming its files.
        const face = join(dir, `${subset}-${weight}${style === "italic" ? "-italic" : ""}.css`);
        const woff2 = existsSync(face)
          ? /url\(\.\/files\/([^)]+\.woff2)\)/.exec(readFileSync(face, "utf8"))?.[1]
          : undefined;
        if (!woff2)
          fail(`widget.toml: font "${font.family}" has no ${style} ${weight} in subset ${subset} (${pkg}@${version})`);
        copyFileSync(join(dir, "files", woff2), join(outDir, "fonts", basename(woff2)));
        files.push(`fonts/${basename(woff2)}`);
        // block: the font is beside the view and cached forever, so a moment's
        // wait beats drawing in a fallback and reflowing.
        css += `@font-face{font-family:"${font.family}";font-style:${style};font-weight:${weight};font-display:block;src:url(fonts/${basename(woff2)}) format("woff2")}\n`;
      }
  (extras.fonts ??= []).push({ family: font.family, source: `${pkg}@${version}`, license: licenseOut, files });
  extras.fontFaces = (extras.fontFaces ?? "") + css;
}

if (request.icon) {
  // Only after bundling: once this process has imported lucide, Bun.build
  // in it no longer tree-shakes lucide (seen with Bun 1.4.0).
  let icons: Record<string, unknown> & {
    IconRegistry: new () => { add(i: Record<string, unknown>): void; get(n: string): unknown };
  };
  try {
    icons = (await import(Bun.resolveSync("@widgetd/sdk/icons", viewDir))) as typeof icons;
  } catch (e) {
    fail(
      `widget.toml: icon = "${request.icon}" needs @widgetd/sdk (with /icons) among the view's dependencies: ${(e as Error).message}`,
    );
  }
  const registry = new icons.IconRegistry();
  registry.add(icons);
  const node = registry.get(request.icon) as Array<[string, Record<string, string | number>]> | undefined;
  if (!node) fail(`widget.toml: icon "${request.icon}" isn't a Lucide icon; see https://lucide.dev/icons for names`);
  const shapes = node.map(([tag, attrs]) => ({
    tag,
    attrs: Object.fromEntries(Object.entries(attrs).map(([k, v]) => [k, String(v)])),
  }));
  extras.icon = { name: request.icon, shapes };
}

await Bun.write(extrasPath, JSON.stringify(extras));
