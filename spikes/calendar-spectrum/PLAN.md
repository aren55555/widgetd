# Calendar widget: plan

The UI is settled in `index.html` here (published as
https://claude.ai/artifact/BfWoTzGjv7tp6nKJfnJHSQ). Its `DEFAULTS` object holds
every agreed setting; port those rather than re-deciding them.

## Sequence

Accounts are a host concern since `ff61937` (accounts and providers): a widget
declares `[[uses]]`, the host signs in, keeps the grant to exactly what
bound widgets use, refreshes, revokes, and signs `widget.Authorize`d
fetches only when a declared scope covers them (`host/providers/google.toml`).
The widget never sees a token or knows which account it has, so this widget
has **no auth code at all**.

0. **Setup (no code).** Admin page → Accounts → Google: register a Web
   application OAuth client with the redirect URI it shows
   (`https://<machine>.<tailnet>.ts.net/<base>oauth/callback`), enable the
   Calendar API, publish the consent screen. Sign-in is a QR code to Google's
   consent page; the phone that scans it must be on the tailnet, since
   Google redirects it back to the host.
1. **Host: nothing.** Full bleed already works: the board adds the title row
   only `if (m.title)` (`client/src/board.ts`) and the iframe is flush to the
   card. Mount with an empty `Title`. (The frame's accessible name then
   falls back to the card id. A re-mount that switches between empty and
   non-empty titles doesn't add or remove the row, which doesn't matter
   here.) Board options' screen-wide `hide_titles` would sit beside this.
2. **Widget server (`examples/calendar-spectrum`), built to WASM (.wpkg):**
   - `widget.toml`: `[network] hosts = ["www.googleapis.com"]`, GET;
     `[[uses]] provider = "google"`, `scopes = [calendar.events.readonly]`
     (covers `calendar/v3/calendars/…`: events and the calendar's time zone).
   - Config: `calendars` (ids, default `primary`), working hours, detail
     level. Calendar ids are typed in for v1; a pick-list via `WithChoices`
     needs `calendarList`, i.e. the broader `calendar.readonly`.
   - Run: poll `calendars/{id}/events?singleEvents=true&orderBy=startTime`
     over a 24-day UTC window: `floor_utc_day(now) − 8d` to `+ 16d`. That covers
     "this Sunday + 14 days" in any display zone (±14 h), and only moves once
     a day. Snapshot on change (`proto.Equal`). **No time zones and no
     midnight logic on the server** (no `time/tzdata` in the WASM): per
     `docs/board-options.md`, `Run` is shared by every screen and never sees
     prefs; payloads carry UTC and the view converts.
   - Errors: 401 is handled by the host (account lapses, instance waits);
     `*widget.ScopeError` means a scope is missing from `[[uses]]`.
   - Table tests against fixture JSON (all-day, declined, tentative,
     overlaps, window edges).
3. **Widget view:** port `<cal-day>`, `<cal-detail>` and `<cal-event>` and
   their defaults; layout and timing as pure functions with bun tests. Days,
   the Sunday-aligned window, midnight rollover, "now", and every time and
   date label come from `ctx.prefs` (time_zone, locale, hour_cycle: "9a" vs
   "09"). DST days are 23 or 25 h; test them in the view.
   Depends on board options' `ctx.prefs`. Until it lands, read the same
   shape from a local `detectPrefs()` (Intl), the fallback the SDK will use
   anyway, and swap to `ctx.prefs` after.

## Agreed behaviour (summary)

- Full-bleed 7×2 grid, Sunday top-left, no card title; solid bands; busy
  cyan, tentative violet, double-booked orange; working-hours band with ticks
  and hour labels only at the Mon–Fri block's outer corners, on the weekend
  side; weekend columns half width, collapsed to label width when empty.
- Day labels use greys, never an event colour. Today's label is white
  (#fff name, #d4d4d8 number) at full opacity. Other weekdays are #a1a1aa
  name and #52525b number; weekends are #71717a and #3f3f46.
- Tap a day: it morphs into a horizontal timeline that fills the card; bars
  fill the height and are solid, with the title and time in dark ink on the bar (sideways on tall narrow bars, truncated with …). Back: "‹ 2 weeks".
- Tap a bar (one tap, no popover): the event screen dissolves in radially from the bar
  (guests, organizer, where, video, notes). Back: "‹ Wed 11". Escape and a
  20 s idle timeout also close.

## API draft

```proto
// Everything in UTC; the view cuts it into days in the display's zone.
message State {
  int64 window_start = 1;          // unix seconds, a UTC midnight
  uint32 resolution_minutes = 2;   // 5
  WorkingHours working_hours = 3;  // wall clock, applied in the display's zone
  DetailLevel detail = 4;          // BUSY | TITLES | FULL (config)
  repeated uint32 busy = 5;        // packed [start − prev start, duration], units from window_start
  repeated uint32 tentative = 6;
  repeated AllDay all_day = 7;
  repeated Event events = 8;       // busy then tentative, span order; empty at BUSY
}
message AllDay {                   // dates, not instants: zone-free
  int32 first_day = 1;             // days since epoch
  uint32 days = 2;
  string title = 3;
}
message Event {
  string title = 1;
  string where = 2; string video = 3; string organizer = 4; string notes = 5;  // FULL; notes ≤ 300
  Response you = 6;
  repeated Guest guests = 7;       // FULL; ≤ 30, names only
  uint32 guest_count = 8;
}
```

Sizes for the fake fortnight: busy ≈ 0.2 KB, titles ≈ 1.2 KB, full ≈ 12 KB.
The UTC window is 24 days (the past days of this week are kept for the dimmed past), so expect about 1.7× that.

## Open questions

- One account per provider per instance: a work + personal view is two
  instances. One merged card across two Google accounts isn't possible
  without a host change.
- Default detail level: suggested TITLES, FULL opt-in.
- Working hours are config (per instance, shared by screens) but applied in
  each display's zone. Fine while the display and the calendar share a zone.
- Event colours stay in the view for now; board options' theming TODO may
  supply them later as CSS custom properties.
- Event fields map from the API: summary, location, hangoutLink /
  conferenceData, organizer, description, attendees[] (responseStatus, self).

## Built (worktree-calendar-widget)

`examples/calendar`, as interviewed on 2026-11-10:
- Calendars: a multi-pick from the account's calendar list (`WithChoices`),
  so the scope is `calendar.readonly`. Empty means `primary`.
- Detail: FULL by default; TITLES and BUSY are settings.
- Declined events are hidden; events marked Free draw as tentative.
- No sync token: Google refuses one alongside timeMin/timeMax, and without
  a window the first sync would pull all history. Each poll (1 min)
  re-reads the 24-day window, trimmed with `fields=`, and snapshots only on
  change.
- View: model.ts (zones, days, layout; bun tests) + view.ts (the three
  components). Prefs come from `detectPrefs()` until board options lands.

## Next: push

You chose push from Google (`events.watch`). The notification only says
"changed", so it triggers the same window re-read; polling drops to a slow
fallback (e.g. 15 min). Needs, in the host: an inbound webhook path for
widgets (a per-instance secret URL, delivered to the widget like actions),
channel renewal before expiry, and a public HTTPS URL for that path only
(Tailscale Funnel on `/hooks/`).
