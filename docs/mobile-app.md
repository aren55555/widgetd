# widgetd display app (Flutter): plan

Status: planned, not started.

## Context
We want a Flutter app on the Play Store and App Store, for phones and tablets, that acts as one more widgetd display. The app keeps a list of "sessions". Opening one shows the board page full screen in a WebView.

The goal is the **simplest** design:
- Everything about rendering, pairing and offline state lives in the web board. A plain browser and the app then behave the same.
- The app is a thin native shell: a session list, settings, and a WebView.
- The app never handles admin passkeys. Admins approve screens from their own phone, as they do today.

## Decisions
- **Pairing stays 100% web.** `<widget-board>` already does it: QR code + short code → `AwaitPairing` → `CompletePairing` sets the HttpOnly `widgetd_session` cookie, valid 365 days (`client/src/board.ts:177`, `host/auth.go:538`).
  - The app only needs a persistent WebView cookie store.
  - No server or auth changes.
- **The device credential lives in the WebView cookie jar.** Consequences we accept:
  - At most one session per server origin. The app rejects a duplicate origin.
  - Clearing app data means re-pairing.
- **Typed query-parameter contract.** Layout options are "function arguments" to the board page, read from `location.search`.
  - Defined once as a protobuf message, `widgetd.v1.BoardOptions`. buf generates the Go, TS and Dart code.
  - Passed as one string-encoded query parameter, `?o=…`.
- **Offline.** The board persists its last-live time. While disconnected, it shows a cached-data badge.

## Web changes (client/)
1. **`proto/widgetd/v1/board.proto` (new): the contract.**
   - Message `BoardOptions { optional uint32 cols = 1; optional google.protobuf.Duration stale_after = 2; }`.
   - protovalidate rules: `cols` between 1 and 12, `stale_after` at least 5 s.
   - **TODO (later): theming through the query parameter.** Add a `Theme` message carrying the `THEME_TOKENS` values, which the board applies as CSS custom properties on itself. The same TODO covers the hard-coded presentation values: `gap`, `row_height`, `padding`, `hide_add_tile` and `hide_titles`. Until then, these and the theme stay whatever the hosting page and `STYLE` in `board.ts` set.
   - Encoded as one query parameter, in the most compact form: `?o=<unpadded base64url(binary BoardOptions)>`. Default values are never written, so an empty message gives no `o` at all. No compression: for messages this small, deflate headers cost more than they save.
   - `buf.gen.yaml` gains a Dart output (`app/lib/gen`), using `protoc-gen-dart` from the pinned mise toolchain. Go and TS output come for free.
   - Old boards ignore fields added later (proto unknown fields), so options stay forward-compatible.
2. **`client/src/lib/options.ts` (new):**
   - `readBoardOptions(location.search)` decodes and validates with `@bufbuild/protovalidate`, which is already a client dependency.
   - Invalid input is ignored, so a bad link still renders the default board.
   - `encodeBoardOptions(opts)` builds the `o` value.
   - A shared fixture `client/src/lib/options.fixture.json` holds `{o, json}` pairs, where `json` is protojson. The TS and Dart tests both decode each `o` and compare it to `json`, so the two languages stay in step.
3. **`client/src/board.ts`:**
   - Merge `readBoardOptions(location.search)` over the attributes. A URL parameter overrides an attribute.
   - Apply `cols` to the existing `maxCols` getter.
4. **Stale badge in `board.ts` + `lib/store.ts`:**
   - On every received frame or keepalive, record `lastLive` in `localStorage` (`widgetd:lastLive`), throttled to once every 10 s.
   - If the stream has been down for more than `staleAfter`, show a corner badge: "Offline · last update 14:32" (or a date if it was another day).
   - Remove the badge on the first live frame.
   - Replace the existing `setStatus("reconnecting…")` text with this badge.
5. **Tests:** `client/src/lib/options.test.ts` runs the fixture. Then run `just client-build`, because `dist/` is committed.

## App (new `app/`, Flutter)
- **Packages:** `webview_flutter`, `wakelock_plus`, `shared_preferences`, `url_launcher`.
- **`lib/session.dart`:**
  - `Session {id, name, baseUrl, BoardOptions options, keepAwake, awakeFrom/awakeTo?, orientation, autoLaunch}`, stored as JSON in shared_preferences.
  - At most one session has `autoLaunch`.
- **`lib/board_options.dart`:** wraps the generated `BoardOptions`, plus `toQuery()` (`o=` + base64url of `writeToBuffer()`). Its test reads `../client/src/lib/options.fixture.json`. The session JSON stores the options as protojson.
- **`lib/ui/session_list.dart`:** list, add, edit, delete. On app start, opens the `autoLaunch` session if there is one.
- **`lib/ui/session_edit.dart`:**
  - Native form for name and URL (reject a duplicate origin).
  - Columns and the stale threshold. These go into the URL.
  - Keep-awake (always, or a time window), orientation, auto-launch. These stay native.
- **`lib/ui/display.dart`:**
  - Loads `baseUrl + toQuery(options)`, with JS and DOM storage enabled.
  - Immersive full screen; applies the wakelock and orientation settings.
  - Off-origin navigation opens in the system browser via `url_launcher`.
  - Reloads when the WebView process dies.
  - Android back button or a 2-second long-press in the top-left corner returns to the list.
- **Platform config:**
  - iOS: `NSAllowsArbitraryLoadsInWebContent`, `NSLocalNetworkUsageDescription`, `UIRequiresFullScreen`, `PrivacyInfo.xcprivacy`.
  - Android: allow cleartext traffic (network security config). Call `CookieManager.flush` on pause through a tiny method channel, if the plugin doesn't already flush.
- **Tooling:** add `flutter` to `mise.toml`, plus `just app-test` and `just app-run` recipes. `app/` is not part of the bun workspace.

## Known limitation, to verify early
**Cold start while offline:**
- When the network drops while a display is running, the board already keeps its cached cards. Only the badge is new.
- When the app launches with no network, the page and `board.js` must come from the WebView's HTTP cache. `board.js` is served `no-cache` (`host/server.go` `serveClient`), and card frames are immutable.

Test this on both platforms during milestone 1. If iOS won't load the page from cache, the fallback (as a later milestone) is: the app saves the page HTML and loads it with `loadHtmlString(baseUrl: origin)`.

## Milestones
1. Web: options contract (cols, stale_after), stale badge, tests, `just client-build`.
2. App: session list and form, full-screen display, wakelock and orientation, auto-launch.
3. Test offline cold start on a real iPad and Android tablet. Add the fallback if needed.
4. Store prep: icons, signing, privacy manifest and data-safety form, a public demo server for App Review.

## Verification
- `just test` (Go + TS), including the new `options.test.ts`.
- `cd app && flutter test`: the Dart options tests against the shared fixture.
- Manual:
  1. `just run`, then open `http://localhost:8080/?o=<encoded {cols: 2}>` in a browser (the fixture has an example) and check the layout.
  2. Pair the browser, stop the server, and check the "Offline · last update" badge appears after `staleAfter`.
  3. In the Android emulator (`http://10.0.2.2:8080`) and iOS simulator: add a session, pair it from a phone, kill and relaunch the app, and check it is still paired and draws instantly.
  4. Revoke the device in admin and check the screen goes back to the QR code.

## Defaults taken where the interview left questions open
- **Sessions:** a session is a server URL + `BoardOptions` + native settings. At most one session per server origin, because the pairing cookie is per origin. Deleting a session only forgets it locally; the admin revokes the device on the server if needed.
- **Layout ownership:** chosen per screen, through the URL (only `cols` in v1). There is no card selection or ordering, and nothing is configured centrally on the server.
- **Offline:** a must-have while running (the board keeps cached cards and shows the badge). Best effort on a cold start with no network: verify in milestone 3, and add the native page-cache fallback only if needed.
- **Stale badge:** one badge for the whole board ("Offline · last update HH:MM"). Showing each card's age is a later step.
- **Native-only settings:** keep-awake (always, or a time window), orientation lock, auto-launch. Dimming schedules, periodic reloads and kiosk lock are later steps.
