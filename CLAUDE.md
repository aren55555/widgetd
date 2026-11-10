# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

widgetd is a self-hostable host for small, live, sandboxed UI widgets. It is a Go library (`host/`) plus a standalone server (`cmd/widgetd`). Each widget has a Go server half and a TypeScript view half, and both are generated from one `.proto` schema.

## Commands

Tool versions are pinned in `mise.toml` (go, buf, bun, just). The justfile runs every tool through `mise exec --`.

```sh
just setup          # bun install + buf generate + client bundle + example widgets
just test           # full CI suite (regenerates and rebuilds first)
just run [ARGS]     # standalone server on :8080 (reads ./widgetd.toml, state in ./data)
just build          # bin/widgetd
just generate       # regenerate from proto/ and internal/testwidget/
just client-build   # rebuild client/dist
just examples       # widgetc build examples/bitcoin-spiral
```

Single tests:

```sh
mise exec -- go test -race ./host -run TestStreamAndResume
cd sdk/web && mise exec -- bun test -t "<name pattern>"     # also: client/, examples/*/view/
```

The TS test suites are `client/src/lib/*.test.ts`, `sdk/web/test/*.test.ts` and `examples/*/view/*.test.ts`. `sdk/web/test/icons.test.ts` bundles `sdk/web/test/frameworks/*` (vanilla, Preact, React, Solid, Lit) in a child process (`frameworks/bundle.ts`), because `Bun.build` inside `bun test` stops tree-shaking packages the test has also imported, and runs each bundle in happy-dom. JS is a bun workspace (`client`, `sdk/web`, `examples/*/view`). Use bun, not node. The `protoc-gen-es` plugin is run through `bun` explicitly.

## Generated and committed artifacts

These are checked in. Regenerate them; don't edit them by hand:
- `gen/` and `client/src/gen/`: from `proto/widgetd/v1/*.proto` (`buf.gen.yaml`).
- `internal/testwidget/gen/` and `sdk/web/test/gen/`: the test widget schema, used by the Go host tests and the web SDK tests.
- `client/dist/`: the bundled `<widget-board>` and admin pages. It is committed because `client/embed.go` embeds it into the Go module, and the module's users have no JS toolchain. **After changing `client/src`, run `just client-build`.**
- For each widget: `gen/`, `view/gen/`, `dist/` and `widget_gen.go`, produced by `widgetc build <dir>`.

## Architecture

**Data flow:** a widget's `Run` (server half) → `widget.Host` → host checks schema → `board` → one Connect server stream per screen → `<widget-board>` in the browser → postMessage → a sandboxed card iframe, where the view SDK checks the payload again and renders it.

- **`sdk/go/widget`**: the server-half contract. A widget is a `Spec` whose `Run(ctx, Host)` reaches everything through `Host`: Config, Fetch, KV, Mount/Unmount and Logger. The point of this boundary is that widget code could later run sandboxed (WASM) without changes. Typed helpers: `Mount[S,D]` (snapshot plus deltas) and `MountState[S]` (snapshots only).
- **`host/runner.go`**: each instance's `Run` is restarted with backoff, and panics are recovered. Every run gets a new **epoch**, which invalidates all client cursors. `rawCard` checks every payload against the widget's descriptors, including protovalidate rules (`schema.go`). If a payload fails, the card faults and the payload never reaches a screen.
- **`host/cardlog.go`**: for each card, the host keeps the latest snapshot plus the deltas since it. It never applies a delta itself, because it knows the schema but not the semantics. Once there are `maxDeltas` deltas, it calls the widget's `current()` callback for a fresh snapshot (compaction). `resume()` uses a client's `(epoch, seq)` cursor to decide what to send: nothing, the deltas after the cursor, or the snapshot plus its deltas.
- **`host/board.go` + `stream.go`**: every card and subscriber, under one lock, so a new stream's initial burst and its live frames never overlap or leave gaps. `StreamWidgets` sends the initial frames, then an **empty response marking the end of the initial burst** (the client drops any cached card it wasn't told about), then live frames and keepalives. A subscriber that falls more than `subscriberBuffer` frames behind is dropped and must reconnect with its cursors.
- **`host/view.go`**: card frames are served at `<base>frame/<name>/<digest>` and assets at `<base>v/<name>/<digest>/`. The digest covers the widget's view and schema, so these are immutable and cached forever. The iframe has no `allow-same-origin` and a CSP with `connect-src 'none'`. Views get data only via postMessage from the board.
- **`host/auth.go`**: admins sign in with WebAuthn passkeys. The first admin enrolls through the one-time setup URL logged at startup. Screens pair by showing a QR code that an admin approves, which gives the screen a device session. Revoking a device kills its open streams (`trackStream`/`kick`). `PublicURL` must be HTTPS (localhost http is allowed for development). State lives in SQLite (`modernc.org/sqlite`, `host/db.go`): KV, admins, credentials, sessions and devices.
- **`host/fetch.go`**: the only way out of a widget. It allows only HTTPS, with a timeout and a body cap. Per-widget network policy from `widget.toml [network]` is a TODO (M1b).
- **`client/`**: `board.ts` is the `<widget-board>` custom element. It keeps each card's last accepted state and cursor in localStorage (`lib/store.ts`), keyed by card id and tagged with digest and state type, so reloads draw instantly and resume. `lib/sync.ts` holds the pure resync logic (`deltaFollows`, `staleCards`, …). `admin.tsx` holds the passkey, pairing and settings pages phones open, as Solid components styled with Tailwind (`lib/ui.ts`); `lib/ConfigForm.tsx` builds the settings form from a widget's schema. `client/build.ts` bundles with Bun, running `.tsx` through `babel-preset-solid` first. Per-screen settings come in as `?o=<base64url widgetd.v1.BoardOptions>` (`lib/options.ts`, fixture in `lib/options.fixture.json`): columns, the offline-badge threshold and display preferences. The gear in the footer opens a read-only settings modal.
- **`sdk/web`**: `defineWidget` for views. `bridge.ts` defines the board↔frame postMessage protocol. `CardCore` holds the DOM-free logic so it can be tested without a browser. Views must supply a pure `apply(state, delta)` when they declare a delta type. A view can declare a `local` message for per-screen state (`ctx.local`). The board keeps it next to its type name and hands it over in `init`, so it is ready before the first `update`. `ctx.prefs` holds the screen's resolved display preferences (locale, time zone, clock, units). The board sends them in `init`, and the SDK detects any that are missing (`src/prefs.ts`, `resolvePrefs`). Payloads carry canonical units, and the view converts them. The package has no top-level side effects (`"sideEffects": false`), so keep it that way.
- **Icons** (Lucide only): `@widgetd/sdk/icons` re-exports Lucide at one pinned version. `icon(Sun)` returns an `<svg>`. `<wd-icon name="sun" size="lg">` is a custom element that works in every framework; `defineIcons({ Sun })` registers icons and defines the element on first call, so a view bundles only what it registers. `IconRegistry` matches Lucide names (exactly, then ignoring dashes if that is unambiguous: `arrow-down-0-1` vs `ArrowDown01`). The element reflects properties to attributes because Solid sets properties. Stroke follows the `--wd-icon-stroke` theme token. JSX types: `@widgetd/sdk/jsx/{preact,react,solid}`. A widget's own icon (`icon` in `widget.toml`) is resolved by `widgetc` through the same registry, stored as shape data in the `.wpkg`, checked against an allowlist (`host/icon.go`), and drawn by `client/src/lib/icon.ts` with `createElementNS`, never as markup, because the board and admin pages aren't sandboxed.

## Adding or changing a widget

A widget folder (see `examples/bitcoin-spiral`, documented in `cmd/widgetc/main.go`) contains `widget.toml` (name, version, title, optional Lucide `icon`, fully qualified `config` message, `[network]`), `buf.yaml`/`buf.lock`, `proto/` (State, Delta, Config), `server/` (a package exporting `Run`) and `view/view.ts` (+ optional `view.css` and `plugins.ts`, which exports Bun build plugins, e.g. `@widgetd/sdk/build/solid` for Solid JSX). `widgetc` generates its buf template inline, so a widget folder has no `buf.gen.yaml`. It bundles the view with `cmd/widgetc/buildview.ts` (embedded): an IIFE, not ESM, because module scripts in an opaque origin would need CORS; minified, with `NODE_ENV=production`; and the build fails if it emits anything but `view.js`, since a card can load nothing else. Built-in widgets are registered in `builtin()` in `cmd/widgetd/main.go`. Instances are configured in `widgetd.toml` (gitignored), with `config` given as protojson.
