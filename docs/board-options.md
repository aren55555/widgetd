# Board options and display preferences: plan

Status: done. The first milestone of [the display app plan](mobile-app.md). It is useful without the app, because every option works the same in a plain browser.

## Context
A screen needs a few per-screen settings: how many columns to use, when to say it's offline, and which units, time zone and clock to show. We want them:
- **Platform-agnostic.** The web board does all the work, so a browser kiosk and the Flutter app render the same.
- **Typed.** One protobuf contract, generated for Go, TS and Dart.
- **Passed in like function arguments.** The options travel in the page URL.
- **Safe when unset.** The display itself detects sensible defaults.
- **Visible.** A read-only settings modal shows each value and where it came from.

## The contract
`proto/widgetd/v1/board.proto` (new):

```proto
message BoardOptions {
  // Maximum grid columns. Unset: the element's cols attribute, otherwise 4.
  optional uint32 cols = 1 [(buf.validate.field).uint32 = {gte: 1, lte: 12}];
  // How long the stream may be down before the offline badge shows. Unset: 30 s.
  google.protobuf.Duration stale_after = 2 [(buf.validate.field).duration.gte = {seconds: 5}];
  Preferences prefs = 3;
  // Space below the footer line, in CSS pixels, so it clears a bezel, a
  // rounded corner or an overlaid system bar. Unset: 16.
  optional uint32 footer_inset = 4 [(buf.validate.field).uint32.lte = 256];
  // TODO: theming (a Theme message carrying THEME_TOKENS, applied as CSS
  // custom properties on the board), and gap, row_height, padding,
  // hide_add_tile, hide_titles. Until then these come from the hosting page
  // and STYLE in board.ts.
}
```

`proto/widgetd/v1/prefs.proto` (new; its own file, because it is also generated into the SDK):

```proto
// Unset or *_UNSPECIFIED means: detect on the display.
message Preferences {
  optional string locale = 1;     // BCP 47, e.g. "en-CA"
  optional string time_zone = 2;  // IANA, e.g. "America/Toronto"
  HourCycle hour_cycle = 3;       // H12 | H24
  TemperatureUnit temperature = 4; // CELSIUS | FAHRENHEIT
  SpeedUnit speed = 5;            // KPH | MPH | MPS
  MassUnit mass = 6;              // KG | LB
  DistanceUnit distance = 7;      // KM | MI
}
```

- **Encoding:** the most compact form, a single `?o=<unpadded base64url(binary BoardOptions)>`. Fields left at their defaults are not written, so an empty message produces no `o` at all. No compression: for messages this small, deflate headers cost more than they save. Example: `{cols: 3, stale_after: 60s}` → `?o=CAMSAgg8`.
- **Forward compatibility:** older boards ignore fields added later (proto unknown fields).
- **Codegen:** `buf.gen.yaml` adds `sdk/web/src/gen` for `prefs.proto`. When the app exists, it also adds Dart output (`app/lib/gen`).

## Reading options (`client/src/lib/options.ts`, new)
- `readBoardOptions(location.search)` decodes `o` and validates it with `@bufbuild/protovalidate`, which is already a client dependency.
- If `o` doesn't decode or fails validation, it is ignored as a whole, so a bad link still shows the default board.
- `encodeBoardOptions(opts)` builds the `o` value.
- Precedence: URL > element attribute > default.
- `client/src/lib/options.fixture.json` holds `{o, json}` pairs (`json` is protojson). The TS tests check against it now, and the Dart tests will later, so the encodings can't drift.

## Detecting defaults on the display (`sdk/web/src/prefs.ts`)
As built, `resolvePrefs` lives in the web SDK (`@widgetd/sdk/prefs`), not in `client/`: the SDK runs the same detection when an older board sends no prefs, and the SDK can't import from the client. `client/src/lib/prefs.ts` holds the board-only parts (the modal's rows and the offline badge's text).

`resolvePrefs(partial, env)` is a pure function. `env` is `{language, timeZone}`, so tests don't need a browser. It returns every field filled in, together with each field's **source**: `set` (from the URL), `detected` (with the evidence), or `default`.

It follows the ICU/CLDR convention used by `Intl`, iOS and Android:
- **`locale`:** `navigator.language`, otherwise `en-US`.
- **`time_zone`:** `Intl.DateTimeFormat().resolvedOptions().timeZone`, otherwise `UTC`. A zone `Intl` rejects counts as unset.
- **`hour_cycle`:** `Intl.DateTimeFormat(locale, {hour: "numeric"}).resolvedOptions().hourCycle`.
- **Units come from the locale's region,** through CLDR's per-region unit preferences (`unitPreferenceData`). Examples: CA → °C, km/h, kg, km; US → °F, mph, lb, mi; GB → °C, mph, kg, mi. Steps:
  1. Read the region. Unicode locale extensions take priority over the CLDR table: `-u-rg-` (region override), `-u-ms-` (`metric`, `ussystem` or `uksystem`) and `-u-mu-` (temperature unit).
  2. A locale without a region is expanded first with `Intl.Locale#maximize()`.
  3. If no region can be found, use metric.

Known limit: a WebView may report only the device *language* (e.g. `en-US` on a tablet whose region is set to Canada). Verify this when building the app. If it happens, the app passes the OS's full locale string as `prefs.locale`, as a fact about the device, and the defaults are still worked out by the board.

## Preferences in widgets (typed)
- `sdk/web/src/bridge.ts`: the `init` message gains `prefs` (the bytes of the resolved `Preferences`).
- `sdk/web/src/index.ts`:
  - `WidgetContext` gains `readonly prefs: DisplayPrefs`.
  - `type DisplayPrefs = Resolved<Preferences>`: every field required, with each enum's `*_UNSPECIFIED` value removed at the type level. Exhaustive `switch`es then break at compile time when a unit is added.
  - The SDK decodes and checks `prefs` before `mount`.
  - If the board sends no `prefs` (an older board), the SDK runs the same browser-based detection itself, so `ctx.prefs` is always filled in.
- **Convention:** payloads always carry canonical units (°C, m/s, kg, m, UTC timestamps). The view converts. A widget's Go `Run` never sees prefs, because it is shared by every screen.
- Preferences are set once at `init`. Changing one means a new URL, which reloads the page.

## Offline badge (`board.ts` + `lib/store.ts`)
- While live, store `lastLive` in `localStorage` (`widgetd:lastLive`), at most once every 10 s.
- When the stream has been down longer than `stale_after`, show a corner badge: "Offline · last update 14:32". It is formatted with the resolved locale, time zone and hour cycle, and shows a date when the last update was on another day.
- The badge goes away on the first live frame. It replaces the current `setStatus("reconnecting…")` text.

## Settings footer and modal (`board.ts`)
- **Footer:** after all cards (after "+ Add widget"), one line with a gear button (inline SVG, in the muted ink of `.link`).
- **Modal:** a `<dialog>` in the board's shadow root, opened with `showModal()`. It closes with Esc, the × button, or a backdrop click. It is **read-only**; editing is deferred.
- **Top-right corner:** a pencil **Edit** button next to ×.
  - It opens `adminUrl` in a new tab with `window.open(url, "_blank", "noopener")`. `adminUrl` comes from `GetSessionResponse.admin_url` and is already stored in `WidgetBoard.adminUrl`.
  - The button is hidden while `adminUrl` is empty.
  - In the app, new-window requests open in the system browser.
- **Content:** the rows come from the same resolved result the board uses for rendering, so the modal can't disagree with it.
  - **Layout:** Columns, Offline badge after, Footer inset.
  - **Preferences:** Language & format, Time zone, Clock, Temperature, Speed, Weight, Distance. Each shows its value in readable form and its source (`set`, `detected · Canada`, `default`).
  - **About:**
    - Board version: the `client/package.json` version, stamped into `board.js` by a `define` in `client/build.ts`.
    - App version: only inside the app, parsed from the `widgetd-display/<version>` user-agent suffix. The row is hidden in a plain browser.

## Verification
- `just test`, plus new `options.test.ts` and `prefs.test.ts`:
  - The fixture round-trips.
  - Invalid `o` is ignored.
  - Region → units checks for CA, US, GB, a bare `en`, and the `-u-ms-` and `-u-mu-` overrides.
  - A bad time zone falls back.
- SDK test (`sdk/web/test/core.test.ts`): `ctx.prefs` is filled in both with and without `init.prefs`.
- `just client-build` (`dist/` is committed).
- Manual, with `just run`:
  1. Open `/` with no `o`. The gear modal shows detected values.
  2. Open `/?o=<fixture value>`. The columns change and the modal shows `set`.
  3. Pencil opens the admin page in a new tab.
  4. Stop the server. The offline badge appears after `stale_after`.
