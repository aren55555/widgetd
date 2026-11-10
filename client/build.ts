// Bundles the board and the admin pages into dist/ (run by `just client-build`).
//
// The admin pages are Solid components (.tsx). Solid compiles JSX into direct
// DOM calls with its Babel preset, which Bun's bundler doesn't run itself, so
// .tsx files go through Babel here first. Everything else is plain Bun.

import { transformAsync } from "@babel/core";
// @ts-expect-error: no type declarations
import solid from "babel-preset-solid";
// @ts-expect-error: no type declarations
import typescript from "@babel/preset-typescript";
import type { BunPlugin } from "bun";

import pkg from "./package.json" with { type: "json" };

const solidJSX: BunPlugin = {
  name: "solid-jsx",
  setup(build) {
    build.onLoad({ filter: /\.tsx$/ }, async ({ path }) => {
      const out = await transformAsync(await Bun.file(path).text(), {
        filename: path,
        babelrc: false,
        configFile: false,
        // Presets run last to first: strip types, then compile JSX.
        presets: [[solid, { generate: "dom" }], [typescript, {}]],
      });
      if (!out?.code) throw new Error(`babel produced nothing for ${path}`);
      return { contents: out.code, loader: "js" };
    });
  },
};

const result = await Bun.build({
  entrypoints: ["src/board.ts", "src/admin.tsx"],
  outdir: "dist",
  target: "browser",
  format: "esm",
  minify: true,
  plugins: [solidJSX],
  // The settings modal shows it as the board version.
  define: { __BOARD_VERSION__: JSON.stringify(pkg.version) },
});
if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}
