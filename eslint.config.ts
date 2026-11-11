// ESLint for every hand-written TypeScript file: the board and admin pages
// (client/), the view SDK (sdk/web/), the example views and widgetc's
// bundler script. Strict, and in the spirit of Google's TypeScript style
// guide (https://google.github.io/styleguide/tsguide.html): typescript-eslint's
// strict and stylistic type-checked sets, plus Google's naming, export and
// equality rules. Formatting is Prettier's job (.prettierrc.json), so the
// formatting rules are off here.
//
//   just lint       # Go and TS
//   just lint-fix   # apply the fixes the linters know how to make

import js from "@eslint/js";
import prettier from "eslint-config-prettier";
import solid from "eslint-plugin-solid/configs/typescript";
import { defineConfig, globalIgnores } from "eslint/config";
import tseslint from "typescript-eslint";

export default defineConfig(
  globalIgnores([
    "**/node_modules/",
    "**/dist/",
    // Generated from .proto files: regenerate, don't edit.
    "**/gen/",
    "examples/.widgetc/",
    "spikes/",
    "bin/",
    "data/",
    // Other checkouts of this repo (git worktrees), gitignored. Prettier
    // skips them by reading .gitignore; ESLint doesn't read it.
    ".claude/worktrees/",
  ]),

  js.configs.recommended,
  tseslint.configs.strictTypeChecked,
  tseslint.configs.stylisticTypeChecked,
  {
    languageOptions: {
      parserOptions: {
        // tsconfig.lint.json covers every linted file.
        project: "./tsconfig.lint.json",
        tsconfigRootDir: import.meta.dirname,
      },
    },
    linterOptions: {
      reportUnusedDisableDirectives: "error",
    },
    rules: {
      // ---- Google: language features ----
      "no-var": "error",
      "prefer-const": "error",
      "one-var": ["error", "never"],
      // `== null` is the one sanctioned loose comparison: it covers undefined.
      eqeqeq: ["error", "always", { null: "ignore" }],
      "guard-for-in": "error",
      "no-new-wrappers": "error",
      "no-eval": "error",
      "no-implied-eval": "off",
      "@typescript-eslint/no-implied-eval": "error",
      "no-caller": "error",
      "no-throw-literal": "off",
      "@typescript-eslint/only-throw-error": "error",
      "prefer-arrow-callback": "error",
      "no-restricted-syntax": [
        "error",
        {
          selector: "ExportDefaultDeclaration",
          message: "Google style: use named exports, not default exports.",
        },
        {
          selector: "TSEnumDeclaration[const=true]",
          message: "Google style: use a plain enum, not a const enum.",
        },
      ],
      // `declare module "react" { namespace JSX … }` is how JSX types are
      // augmented; real namespaces are still out.
      "@typescript-eslint/no-namespace": ["error", { allowDeclarations: true }],
      // An async event handler (`onClick={async () => …}`) is idiomatic in
      // Solid. Elsewhere a promise where a void is expected is still an error.
      "@typescript-eslint/no-misused-promises": ["error", { checksVoidReturn: { attributes: false } }],
      "@typescript-eslint/no-require-imports": "error",
      // `public` is the default: Google style leaves it off.
      "@typescript-eslint/explicit-member-accessibility": ["error", { accessibility: "no-public" }],
      // `T[]` for simple element types, `Array<…>` for anything more.
      "@typescript-eslint/array-type": ["error", { default: "array-simple" }],
      "@typescript-eslint/consistent-type-definitions": ["error", "interface"],
      "@typescript-eslint/consistent-type-imports": "error",
      "@typescript-eslint/consistent-type-exports": "error",
      "@typescript-eslint/switch-exhaustiveness-check": ["error", { considerDefaultExhaustiveForUnions: true }],
      // Google style allows `!` where a value is known to be there (a view's
      // own template: `root.querySelector(".label")!`).
      "@typescript-eslint/no-non-null-assertion": "off",
      // `() => el.remove()` as a callback is clear enough.
      "@typescript-eslint/no-confusing-void-expression": ["error", { ignoreArrowShorthand: true }],
      // `s || "default"` deliberately treats "" (proto3's unset string) as
      // missing; `??` wouldn't.
      "@typescript-eslint/prefer-nullish-coalescing": ["error", { ignorePrimitives: { string: true } }],
      "@typescript-eslint/restrict-template-expressions": ["error", { allowNumber: true }],
      // `() => {}` is the idiomatic no-op callback.
      "@typescript-eslint/no-empty-function": ["error", { allow: ["arrowFunctions"] }],
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_", caughtErrors: "none" },
      ],

      // ---- Google: naming ----
      "@typescript-eslint/naming-convention": [
        "error",
        // lowerCamelCase by default…
        { selector: "default", format: ["camelCase"] },
        // …imports keep the name the module gave them (Lucide icons, React)…
        { selector: "import", format: null },
        // …module-level constants may be CONSTANT_CASE, and components and
        // other values that act as classes UpperCamelCase.
        {
          selector: "variable",
          format: ["camelCase", "UPPER_CASE", "PascalCase"],
        },
        // Build-time defines (`__BOARD_VERSION__`) are spelled the bundler's way.
        {
          selector: "variable",
          modifiers: ["global"],
          filter: { regex: "^__[A-Z_]+__$", match: true },
          format: null,
        },
        { selector: "function", format: ["camelCase", "PascalCase"] },
        { selector: "parameter", format: ["camelCase"], leadingUnderscore: "allow" },
        { selector: "typeLike", format: ["PascalCase"] },
        { selector: "enumMember", format: ["PascalCase", "UPPER_CASE"] },
        // No `I` prefix on interfaces.
        {
          selector: "interface",
          format: ["PascalCase"],
          custom: { regex: "^I[A-Z]", match: false },
        },
        // Object keys and properties often mirror wire or DOM names
        // ("Content-Type", "aria-label", proto JSON): leave them be.
        {
          selector: ["objectLiteralProperty", "typeProperty", "objectLiteralMethod"],
          format: null,
        },
        { selector: "classProperty", modifiers: ["static", "readonly"], format: ["camelCase", "UPPER_CASE"] },
      ],
    },
  },

  // Plain JavaScript config files aren't in a TS project.
  {
    files: ["**/*.js", "**/*.mjs"],
    extends: [tseslint.configs.disableTypeChecked],
  },

  // Solid's reactivity rules, for the admin pages and Solid views. Not for
  // every .tsx: the SDK's framework tests also hold Preact and React.
  {
    ...solid,
    files: ["client/src/**/*.tsx", "examples/*/view/**/*.tsx", "sdk/web/test/frameworks/solid.tsx"],
    rules: {
      ...solid.rules,
      // Flags every prop read outside JSX, including the deliberate
      // read-once ones (an editor seeded from its initial values).
      "solid/reactivity": "off",
    },
  },

  // Tests
  {
    files: ["**/*.test.ts", "**/*.test.tsx", "sdk/web/test/**"],
    rules: {
      // Stub views implement methods they don't need, and async stubs
      // needn't await anything.
      "@typescript-eslint/no-empty-function": "off",
      "@typescript-eslint/require-await": "off",
      // bun:test types `await expect(p).rejects.toThrow()` as void, though it
      // returns a promise.
      "@typescript-eslint/await-thenable": "off",
      "@typescript-eslint/no-confusing-void-expression": "off",
    },
  },

  // Tool entry points whose loader wants a default export.
  {
    files: ["eslint.config.ts", "**/plugins.ts"],
    rules: { "no-restricted-syntax": "off" },
  },

  // Formatting belongs to Prettier.
  prettier,
);
