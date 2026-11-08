# widgetd

A self-hostable host for small, live, sandboxed UI widgets: a price chart, a
game score, anything that updates on its own. Run it on a VPS, an old PC or a
Raspberry Pi, or embed it in an existing Go server.

- **One pipe.** Every card on a screen arrives over a single Connect server
  stream. A reconnect or reload is sent only what changed since the screen
  last saw each card.
- **One schema per widget.** A widget's server half and view half are
  generated from the same `.proto`. The host checks every payload against it
  (including protovalidate rules) before sending, and the view checks it again
  on arrival.
- **Sandboxed both ends.** A widget's server half is a WASI module run by
  [wazero](https://wazero.io): its own memory, no files, no sockets; its only
  way out is a fetch the host checks against a per-widget network policy. Each
  card renders in its own iframe in an opaque origin, with no network access:
  data reaches it only through the stream.
- **Passkeys and paired screens.** Admins sign in with a passkey. A screen
  shows a QR code; an admin approves it from their phone.

## Layout

| Path | What |
|---|---|
| `host/` | The Go library: runs widgets, serves the stream, auth |
| `cmd/widgetd/` | Standalone server (`-acme-domain` for Let's Encrypt) |
| `cmd/widgetc/` | Builds a widget folder: codegen for both halves, view bundle |
| `sdk/go/widget/` | Server-half SDK |
| `sdk/web/` | View-half SDK (`defineWidget`) |
| `client/` | `<widget-board>` and the admin pages (built output committed in `client/dist`) |
| `examples/bitcoin-spiral/` | Example widget, as a folder |
| `examples/sports-game/` | Game cards for a followed team: cards that come and go with each game |
| `examples/clock.widget` | Example widget, as a single file |
| `docs/abi.md` | The sandbox ABI, for writing a widget in another language |

## Develop

Tools are pinned in `mise.toml`; you need [just](https://just.systems).

```sh
just setup   # codegen, client bundle, example widgets
just test    # everything CI runs
just run     # standalone server on :8080
```

## License

Copyright 2026 Aren Patel. Licensed under the [Apache License, Version 2.0](LICENSE).
