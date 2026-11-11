// Bundles one fixture the way widgetc bundles a view and prints it. The test
// runs this in its own process: Bun.build inside `bun test` stops
// tree-shaking a package the test itself has imported (seen with lucide,
// Bun 1.4.0), which would hide exactly what the test checks.

import { join } from "node:path";
import { solid } from "../../src/build/solid.ts";

const fixture = process.argv[2];
const r = await Bun.build({
  entrypoints: [join(import.meta.dir, fixture)],
  format: "iife",
  target: "browser",
  minify: true,
  define: { "process.env.NODE_ENV": '"production"' },
  plugins: fixture.startsWith("solid") ? [solid()] : [],
});
if (!r.success) {
  for (const l of r.logs) console.error(l);
  process.exit(1);
}
process.stdout.write(await r.outputs[0].text());
