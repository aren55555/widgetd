# widgetd display (mobile/)

A Flutter app for phones and tablets that shows a widgetd board full screen.
Everything about the board (pairing, cards, offline state) is the web page's;
the app is a thin shell around a WebView. See [docs/mobile-app.md](../docs/mobile-app.md).

- `lib/session.dart`: sessions (server URL + `BoardOptions` + device settings), stored in shared_preferences.
- `lib/board_options.dart`: `?o=` encoding, pinned by `client/src/lib/options.fixture.json`.
- `lib/ui/`: session list, edit form, full-screen display.
- `lib/gen/`: generated from `proto/` by `just generate` (don't edit).

```sh
just mobile-test                     # flutter analyze + unit/widget tests
just mobile-run                      # run on a connected device or simulator

# Against a real server (just run in another terminal):
cd mobile && flutter test integration_test -d <device> \
  --dart-define=WIDGETD_URL=http://localhost:8080/   # Android emulator: http://10.0.2.2:8080/
```

Leave the display: Android's back button, or press the top-left corner for 2 seconds.
