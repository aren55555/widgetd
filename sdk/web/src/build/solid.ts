// A Bun build plugin that compiles Solid's JSX, for a widget's view/plugins.ts:
//
//	// view/plugins.ts
//	import { solid } from "@widgetd/sdk/build/solid";
//	export default [solid()];
//
// Solid compiles JSX into direct DOM calls with its Babel preset, which Bun's
// bundler doesn't run itself. The view adds the compiler to its own
// devDependencies: @babel/core@7, @babel/preset-typescript@7 and
// babel-preset-solid (the same versions client/build.ts uses). This runs at
// build time only; nothing here reaches the card.

import type { BunPlugin } from "bun";

export function solid(opts: { filter?: RegExp } = {}): BunPlugin {
  return {
    name: "widgetd-solid",
    async setup(build) {
      // Loaded here, not at the top, so a view that only imports the types
      // of this module needs none of Babel.
      const { transformAsync } = await import("@babel/core");
      // @ts-expect-error: no type declarations
      const { default: solidPreset } = await import("babel-preset-solid");
      // @ts-expect-error: no type declarations
      const { default: typescript } = await import("@babel/preset-typescript");
      build.onLoad({ filter: opts.filter ?? /\.[jt]sx$/ }, async ({ path }) => {
        const out = await transformAsync(await Bun.file(path).text(), {
          filename: path,
          babelrc: false,
          configFile: false,
          // Presets run last to first: strip types, then compile JSX.
          presets: [[solidPreset, { generate: "dom" }], [typescript, {}]],
        });
        if (!out?.code) throw new Error(`babel produced nothing for ${path}`);
        return { contents: out.code, loader: "js" };
      });
    },
  };
}
