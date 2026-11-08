# The widget ABI

A widget's server half is a WebAssembly module the host runs in a sandbox.
This page is everything a module must do, so you can write one in any
language that targets `wasm32-wasip1` and can encode protobuf. The Go SDK
(`sdk/go/widget`, `widget.Main`) is one implementation of it.

## What the module is

- A **WASI preview 1 command**. The host runs its `_start` once, and that is
  the widget: a loop that lives as long as the instance is configured. If
  `_start` returns, or the module traps, the host restarts it with backoff.
- It may import from exactly two modules: `wasi_snapshot_preview1` and
  `widgetd`. A module importing anything else is refused at load.
- **WASI gives it:** wall and monotonic clocks, sleep, and random bytes.
  There are no preopened directories, no environment variables, no arguments
  beyond its name, and no sockets. stdout and stderr go to the host's log, a
  line at a time.
- Its memory is capped at 128 MiB. Exceeding it traps the module.

## The two imports

```
(import "widgetd" "call" (func $call (param i32 i32) (result i32)))
(import "widgetd" "take" (func $take (param i32 i32)))
```

- `call(ptr, len) -> n` reads an encoded **`Call`** from `[ptr, ptr+len)`,
  performs it, and returns `n`, the length of the encoded **`Result`**. The
  host holds that Result until the next `take`.
- `take(ptr, n)` writes the held Result to `[ptr, ptr+n)`. `n` must be the
  value `call` returned; anything else traps.

Always `take` after a `call`, before the next `call`. A call can block (a
fetch, for instance) for as long as the network policy allows; the host
never calls into the module, so there is no re-entrancy to handle.

The messages are in [`proto/widgetd/abi/v1/abi.proto`](../proto/widgetd/abi/v1/abi.proto).
A `Result` with `error` set failed; `denied_rule` is also set when the
network policy refused a fetch.

## Calls

| Call | Does | Result |
|---|---|---|
| `ConfigGet` | The instance's configuration, already validated against the widget's `Config` | `config`: bytes of `Config` |
| `Fetch` | An HTTP request, under the network policy (below) | `fetch`: status, headers, whole body |
| `KvGet` / `KvPut` / `KvDelete` | Storage private to this instance, kept across restarts (keys up to 256 bytes, values up to 256 KiB) | `kv`: `found`, `value` |
| `Mount` | Shows a card, or updates one: `id` (local to the instance), title, size in grid cells, optional `visible_until_ms`, and the fully qualified `state_type` / `delta_type` from the widget's schema | empty |
| `Unmount` | Removes a card | empty |
| `Emit` | Sends data for a card (below) | `emit.want_checkpoint` |
| `Log` | A log line at a level, with attributes | empty |
| `TakeActions` | What viewers sent the module's cards since the last call, without waiting. Poll it (the Go SDK does, every 200 ms) if any card was mounted with an `action_type` | `actions`: card id and encoded action, each already checked against the card's type |

## Sending card data

Every payload is the encoded message of the type the card was mounted with.
The host decodes it against the widget's schema, refuses fields the schema
doesn't define, and enforces its protovalidate rules. A refused payload
returns an error, and screens show the card as faulted while keeping its last
good state.

- `KIND_SNAPSHOT` replaces the card's whole state. A card mounted without a
  `delta_type` only ever sends these.
- `KIND_DELTA` sends a change. When the Result has `want_checkpoint`, send
  `KIND_CHECKPOINT` next, with the whole state as of that delta. The host
  keeps a checkpoint plus the deltas since it, so a screen that connects or
  reconnects gets exactly what it lacks. The host never applies a delta
  itself, which is why it asks you for the sum. Ignoring the request is
  tolerated for a while, then deltas are refused until you comply.

## Network policy

`Fetch` is the only way out. The host allows a request only if:

- the widget's manifest asked for the host and method (`[network]` in
  `widget.toml`), **and** whoever runs the host granted it (a grant can only
  narrow the request);
- it is `https` (plain `http` only if explicitly granted);
- the address the name resolves to isn't loopback, private, link-local,
  carrier-grade NAT (which includes tailnets), or otherwise reserved. This is
  checked on the connection itself and again on every redirect;
- it is under the instance's rate limit, and its response under the size
  limit.

The host also strips `Cookie`, `Authorization` and the other headers it owns.

## Packaging

`widgetc build` produces a `.wpkg`, a zip of:

```
manifest.json   name, version, config message, network request, own proto paths
schema.binpb    FileDescriptorSet of the widget's protos and their imports
server.wasm     this module
view/           the built view: view.js, view.css
```

A module built by another toolchain only needs to replace `server.wasm`.

## Helper modes: choices and verify

Besides its main loop, a module can help with its settings form. The host
runs it briefly, with the arguments `[name, mode]`, where `ConfigGet`
returns the settings being edited. It gets a 15-second limit, the same
network policy, and no cards (`Mount` is refused). The module answers with
one call, then exits 0.

| Mode | When | Answer |
|---|---|---|
| `widgetd:choices` | As settings are edited | `Choices`: pick-lists by field name ("team"), each a value and a label. Fields not listed stay free text. |
| `widgetd:verify` | On save, after the schema's rules pass | `Verdict`: violations by field (the save is refused and they're shown beside their fields), or approval, optionally with tidied settings to save instead (which must pass the rules too). |

A module only gets asked for the modes its package declares (`modes` in
`manifest.json`, which widgetc fills in when the server package exports
`Choices` or `Verify`): a module that doesn't know a mode would just start
its main loop. If verify can't reach a verdict (exits non-zero, or times
out), the save is refused with that reason, so it can be retried.
