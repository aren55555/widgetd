# Board options and display preferences

Status: done (`1422447`). The first milestone of [the display app plan](mobile-app.md). It is useful without the app, because every option works the same in a plain browser. This was the plan; it now records the design as built, with notes where the build differs, and the [follow-ups](#follow-ups) left open.

## Context
A screen needs a few per-screen settings: how many columns to use, when to say it's offline, and which units, time zone and clock to show. We want them:
- **Platform-agnostic.** The web board does all the work, so a browser kiosk and the Flutter app render the same.
- **Typed.** One protobuf contract, generated for Go and TS now, and for Dart once the app exists.
- **Passed in like function arguments.** The options travel in the page URL.
- **Safe when unset.** The display itself detects sensible defaults.
- **Visible.** A read-only settings modal shows each value and where it came from.

## The contract
`proto/widgetd/v1/board.proto`:

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

`proto/widgetd/v1/prefs.proto` (its own file, because it is also generated into the SDK):

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

## Reading options (`client/src/lib/options.ts`)
- `readBoardOptions(location.search)` decodes `o` and checks `board.proto`'s rules.
  - **As built:** the rules are checked by hand in `checkRules`, not with `@bufbuild/protovalidate`. Its CEL runtime would more than triple `board.js` for two rules. `options.test.ts` checks that `checkRules` agrees with protovalidate, so the two can't drift. **A new rule in `board.proto` needs a matching line in `checkRules`.**
  - Unknown fields are accepted, because they come from a newer contract.
- If `o` doesn't decode or fails a rule, it is ignored as a whole, so a bad link still shows the default board. The reason is kept (`BoardOptionsRead.error`).
- `encodeBoardOptions(opts)` builds the `o` value.
- Precedence: URL > element attribute > default (`resolveLayout`, which also records each value's source for the modal).
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
- `sdk/web/src/bridge.ts`: the `init` message gains `prefs` (the bytes of the resolved `Preferences`). The board resolves and encodes them once per page load, and sends the same bytes to every card.
- `sdk/web/src/index.ts`:
  - `WidgetContext` gains `readonly prefs: DisplayPrefs`.
  - `DisplayPrefs` (in `sdk/web/src/prefs.ts`): every field required, with each enum's `*_UNSPECIFIED` value removed at the type level. Exhaustive `switch`es then break at compile time when a unit is added.
  - The SDK decodes and checks `prefs` before `mount`.
  - If the board sends no `prefs` (an older board), the SDK runs the same browser-based detection itself, so `ctx.prefs` is always filled in.
- **Convention:** payloads always carry canonical units (°C, m/s, kg, m, UTC timestamps). The view converts. A widget's Go `Run` never sees prefs, because it is shared by every screen.
- Preferences are set once at `init`. Changing one means a new URL, which reloads the page.

### Next to `ctx.local`
`ctx.local` (merged just before this, in PR #1) is the other per-screen value a card gets, and it uses the same path: `init` now carries `{ stored, local, prefs }`, and both values are ready before `mount`. They don't overlap:

|  | `ctx.prefs` | `ctx.local` |
| --- | --- | --- |
| Schema | widgetd's `widgetd.v1.Preferences` | the widget's own `local` message |
| Scope | the whole screen, every card | one card on this screen |
| Set by | the URL (`?o=`), or detection | the widget's view (`ctx.local.set`) |
| Kept in | the URL, plus the screen's copy on the server | the board's localStorage (`widgetd:local:<cardId>`), plus the screen's copy on the server |
| Writable by the widget | no | yes |

A widget shouldn't copy a display preference into `ctx.local`. A unit choice belongs in `?o=` so every card on the screen agrees.

## Saved on the server, and continued by another screen (`39f0196`)
A paired screen keeps a copy of its settings on the server, so a replacement screen can start where it left off.

- **What is saved:** `widgetd.v1.ScreenState` (`proto/widgetd/v1/auth.proto`). It holds the page's `BoardOptions` and, for each card, its view's string preferences (`ctx.storage`) and its typed local value (`ctx.local`, as `local_type` + `local_value`). The board never sends its cached card states, which a new screen gets from the server anyway.
- **When:** `AuthService.SaveScreenState`, 2 s (`SAVE_SCREEN_AFTER`) after the screen connects or after any `storage-set` or `local-set`. A burst of changes becomes one save. Only a screen with a device session saves; an admin's own board doesn't. Failures are logged and retried by the next change or reconnect.
- **Checks on the server:** protovalidate on the whole message, which includes `board.proto`'s rules, and a 1 MiB cap (`maxScreenState`). Stored per device in the `screen_state` table.
- **Continuing a screen:** when approving a pairing, the admin can choose an existing screen instead of naming a new one (`ApprovePairingRequest.continue_device_id`). The new screen then:
  - **shares that device's identity.** Revoking it signs out every screen continuing it.
  - **receives the saved state** in `CompletePairingResponse.screen_state`. `adoptScreen` replaces the listed cards' preferences and local values (`CardStore.putPrefs`). It copies the saved `?o=` into the page URL with `history.replaceState`, unless the URL already has its own `o`, so a reload keeps it.
- **Not a live sync.** The state is copied once, at pairing. Afterwards, the screens sharing a device each save their own state, and the most recent save wins. Pairing a third screen as a continuation starts it from whichever screen saved last.
- **What leaves the screen:** a widget's `ctx.storage` and `ctx.local` values are now stored on the server too, readable only by its paired screens. They still never reach any widget's server half.

## Offline badge (`board.ts` + `lib/store.ts`)
- While live, store `lastLive` in `localStorage` (`widgetd:lastLive`), at most once every 10 s (`LIVE_EVERY`).
- When the stream has been down longer than `stale_after`, show a corner badge: "Offline · last update 14:32" (`offlineText` in `client/src/lib/prefs.ts`). It is formatted with the resolved locale, time zone and hour cycle, and shows a date when the last update was on another day. With no `lastLive` at all, it reads just "Offline".
- The badge goes away on the first live frame. It replaced the old `setStatus("reconnecting…")` text.

## Settings footer and modal (`board.ts`)
- **Footer:** after the grid, one right-aligned line of icon buttons, which are the board's inline Lucide SVGs (`ICONS`):
  - **"Set up widgets"** (QR code, added in `461d700`) shows every card's set-up code at once. Cards of one instance share its code. Pressing the button again, or Esc, takes the codes down, but not sign-in codes. It reads as pressed while any set-up code is up.
  - **"Screen settings"** (gear) opens the modal below.
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
- `just lint` (added after this milestone): Go, `tsc`, ESLint and Prettier.

## Follow-ups
- **Widgets adopting `ctx.prefs`.** `weather` (temperature unit, time zone) and `bart-tracker` (clock) read it. The calendar widget (`examples/calendar/view/model.ts`), added after this milestone, still has its own `detectPrefs()`, and its comment says to switch "until then". It should read `ctx.prefs.locale`, `timeZone` and `hourCycle`, so a screen's `?o=` applies to it. The other views that format times with `Intl` themselves are the next candidates: `clock.widget`, `sports-game` (`game.ts`, `chart.ts`) and `bitcoin-spiral` (`spiral.ts`).
- **Theming and layout knobs** (the `TODO` in `board.proto`): a `Theme` message carrying `THEME_TOKENS`, plus `gap`, `row_height`, `padding`, `hide_add_tile` and `hide_titles`. Each new rule also needs a line in `checkRules`.
- **Editing in the modal.** The modal is read-only. Editing would build a new `o` and reload the page; the app's native form does the same.
- **Per-card age.** The badge covers the whole board. Showing each card's age is a later step.
- **Showing where the options came from.** When `adoptScreen` copies a continued screen's `?o=`, the modal shows those values as `set`, the same as values typed into the URL. A source like `continued from <screen>` would explain it.
