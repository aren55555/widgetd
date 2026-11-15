# widgetd display app (Flutter): plan

Status: milestone 1 (web) is done, and is recorded in [board-options.md](board-options.md). The app itself (milestones 2–4) is not started.

## Context
We want a Flutter app on the Play Store and App Store, for phones and tablets, that acts as one more widgetd display. The app keeps a list of "sessions". Opening one shows the board page full screen in a WebView.

The goal is the **simplest** design:
- Everything about rendering, pairing and offline state lives in the web board. A plain browser and the app then behave the same.
- The app is a thin native shell: a session list, settings, and a WebView.
- The app never handles admin passkeys. Admins approve screens from their own phone, as they do today.

## Decisions
- **Pairing stays 100% web.** `<widget-board>` already does it: QR code + short code → `AwaitPairing` → `CompletePairing` sets the HttpOnly `widgetd_screen` cookie, valid 365 days (the pairing loop in `client/src/board.ts`; `CompletePairing` and `deviceTTL` in `host/auth.go`).
  - The app only needs a persistent WebView cookie store.
  - No server or auth changes.
  - **Continuing a screen** (`39f0196`) is web too. The admin approving the pairing can choose an existing screen instead of a new name. The app's screen then shares that device's identity and starts from its saved settings. This is how a tablet is replaced, or how the app takes over from a browser kiosk. The app needs nothing for it.
- **Account sign-in stays web too.** Since display accounts (`ff61937`), a screen can sign a bound account back in. The board shows a QR code (and, for device-flow providers, a code), and the consent happens on a phone. The display never opens the provider's page, so the app needs nothing native for it.
- **The device credential lives in the WebView cookie jar, and the screen's data in its DOM storage.** Consequences we accept:
  - At most one session per server origin. The app rejects a duplicate origin.
  - Clearing app data means re-pairing. It also drops what the board keeps in localStorage: the cached cards (so the first draw waits for the server) and each card's `ctx.storage` and `ctx.local` values.
  - Re-pairing **as a continuation of the same screen** gets the preferences and local values back from the server's copy (the `ScreenState`, see [board-options.md](board-options.md#saved-on-the-server-and-continued-by-another-screen-39f0196)). The cached cards come back from the stream. Pairing as a new screen starts empty.
- **Typed query-parameter contract.** Layout options are "function arguments" to the board page, read from `location.search`.
  - Defined once as a protobuf message, `widgetd.v1.BoardOptions`. buf generates Go and TS today. The app adds Dart.
  - Passed as one string-encoded query parameter, `?o=…`.
  - **Who owns the options: the app's session, when it sets any.** A continued screen copies the saved `?o=` into its URL only when the URL has no `o` of its own. So:
    - A session with options always sends them, and they win. The board then saves them as the screen's copy.
    - A session with no options sends no `o`, and a continued screen takes the saved one. The app doesn't learn it: the board writes it into the WebView's URL, not the session. The session form shows these fields as "from the screen" rather than blank, and saving the form makes them the session's own. This needs a small bridge, the app reading `o` from the WebView URL after load.
- **Offline.** The board persists its last-live time. While disconnected, it shows a cached-data badge.

## Web changes (client/): done
See [board-options.md](board-options.md). It covers:
- the `BoardOptions` and `Preferences` contract
- defaults detected on the display
- `ctx.prefs` for widgets
- the offline badge
- the read-only settings modal

That is milestone 1. The app adds only the Dart output to `buf.gen.yaml` (`app/lib/gen`) and runs the shared fixture (`client/src/lib/options.fixture.json`) in its own tests.

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
  - Preferences (units, time zone, clock) go into `prefs` only when the user sets them explicitly. Anything left unset is detected by the board.
  - Keep-awake (always, or a time window), orientation, auto-launch. These stay native.
- **`lib/ui/display.dart`:**
  - Loads `baseUrl + toQuery(options)`, with JS and DOM storage enabled.
  - Immersive full screen; applies the wakelock and orientation settings.
  - Off-origin navigation opens in the system browser via `url_launcher`.
  - Reloads when the WebView process dies.
  - Android back button or a 2-second long-press in the top-left corner returns to the list.
- **User agent:** append `widgetd-display/<version>`. The board's settings modal shows this as the app version.
- **Platform config:**
  - iOS: `NSAllowsArbitraryLoadsInWebContent`, `NSLocalNetworkUsageDescription`, `UIRequiresFullScreen`, `PrivacyInfo.xcprivacy`.
  - Android: allow cleartext traffic (network security config). Call `CookieManager.flush` on pause through a tiny method channel, if the plugin doesn't already flush.
- **Tooling:** add `flutter` to `mise.toml`, plus `just app-test` and `just app-run` recipes. `app/` is not part of the bun workspace.
  - `just lint` now exists (Go, `tsc`, ESLint, Prettier). Add `flutter analyze` and `dart format --set-exit-if-changed` to it.
  - Keep `app/` out of the TS lint and Prettier runs.

## Known limitation, to verify early
**Cold start while offline:**
- When the network drops while a display is running, the board already keeps its cached cards. Only the badge is new.
- When the app launches with no network, the page and `board.js` must come from the WebView's HTTP cache. `board.js` is served `no-cache` (`host/server.go` `serveClient`), and card frames are immutable.

This wasn't tested in milestone 1, which was web only, so it moves to milestone 3. If iOS won't load the page from cache, the fallback (as a later milestone) is: the app saves the page HTML and loads it with `loadHtmlString(baseUrl: origin)`.

## Milestones
1. ~~Web: options contract (cols, stale_after), stale badge, tests, `just client-build`.~~ Done (`1422447`), together with display preferences (`ctx.prefs`) and the settings modal.
2. App: session list and form, full-screen display, wakelock and orientation, auto-launch.
3. Test offline cold start on a real iPad and Android tablet. Add the fallback if needed.
4. Store prep: icons, signing, privacy manifest and data-safety form, a public demo server for App Review.

## Verification
- `just test` (Go + TS), including `options.test.ts`, and `just lint`.
- `cd app && flutter test`: the Dart options tests against the shared fixture.
- Manual:
  1. `just run`, then open `http://localhost:8080/?o=<encoded {cols: 2}>` in a browser (the fixture has an example) and check the layout.
  2. Pair the browser, stop the server, and check the "Offline · last update" badge appears after `staleAfter`.
  3. In the Android emulator (`http://10.0.2.2:8080`) and iOS simulator: add a session, pair it from a phone, kill and relaunch the app, and check it is still paired and draws instantly.
  4. Revoke the device in admin and check the screen goes back to the QR code. With two screens continuing one device, check that both go back.
  5. Pair a browser screen, change a card's preference (one using `ctx.local`) and open it with a `?o=`. Then pair the app with no options, approved as a continuation of that screen. Check the card's preference and the options carry over, and that the session form shows the options as "from the screen".
  6. With a widget bound to an account that needs signing in again, check the sign-in QR shows in the app and that scanning it with a phone completes the sign-in.
  7. Set a preference in the session form (e.g. Fahrenheit) and check the settings modal shows it as `set`.

## Defaults taken where the interview left questions open
- **Sessions:** a session is a server URL + `BoardOptions` + native settings. At most one session per server origin, because the pairing cookie is per origin. Deleting a session only forgets it locally; the admin revokes the device on the server if needed. If other screens continue the same device, revoking it signs those out too.
- **Layout ownership:** chosen per screen, through the URL (only `cols` in v1). There is no card selection or ordering, and nothing is configured centrally on the server.
- **Offline:** a must-have while running (the board keeps cached cards and shows the badge). Best effort on a cold start with no network: verify in milestone 3, and add the native page-cache fallback only if needed.
- **Stale badge:** one badge for the whole board ("Offline · last update HH:MM"). Showing each card's age is a later step.
- **Native-only settings:** keep-awake (always, or a time window), orientation lock, auto-launch. Dimming schedules, periodic reloads and kiosk lock are later steps.
