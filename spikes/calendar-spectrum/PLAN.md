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
1. **Host:** a `Bleed` mount option, and the board (`client/src/board.ts`)
   skipping its title row for such cards. (Not there yet.)
2. **Widget server (`examples/calendar-spectrum`), built to WASM (.wpkg):**
   - `widget.toml`: `[network] hosts = ["www.googleapis.com"]`, GET;
     `[[uses]] provider = "google"`, `scopes = [calendar.events.readonly]`
     (covers `calendar/v3/calendars/…`: events and the calendar's time zone).
   - Config: `calendars` (ids, default `primary`), working hours, detail
     level. Calendar ids are typed in for v1; a pick-list via `WithChoices`
     needs `calendarList`, i.e. the broader `calendar.readonly`.
   - Run: `GET calendars/{id}` once for `timeZone`, then poll
     `calendars/{id}/events?singleEvents=true&orderBy=startTime&timeMin&timeMax`
     for the fortnight (Google expands recurrences). Split at local midnight
     with `time.LoadLocation`, which under wasip1 needs `import _
     "time/tzdata"`. Snapshot on change (`proto.Equal`) and at midnight.
   - Errors: 401 is handled by the host (account lapses, instance waits);
     `*widget.ScopeError` means a scope is missing from `[[uses]]`.
   - Table tests against fixture JSON (all-day, cross-midnight, DST,
     declined, tentative, overlaps).
3. **Widget view:** port `<cal-day>`, `<cal-detail>` and `<cal-event>` and
   their defaults; layout and timing as pure functions with bun tests.

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
message State {
  int32 first_day = 1;             // days since epoch; a Sunday
  sint32 utc_offset_minutes = 2;   // the calendar's zone
  uint32 resolution_minutes = 3;   // 5
  WorkingHours working_hours = 4;
  DetailLevel detail = 5;          // BUSY | TITLES | FULL (config)
  repeated Day days = 6;           // 14
}
message Day {
  repeated uint32 busy = 1;        // packed [start − prev start, duration]
  repeated uint32 tentative = 2;
  repeated string all_day = 3;
  repeated Event events = 4;       // span order; empty at BUSY
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

## Open questions

- One account per provider per instance: a work + personal view is two
  instances. One merged card across two Google accounts isn't possible
  without a host change.
- Default detail level: suggested TITLES, FULL opt-in.
- Event fields map from the API: summary, location, hangoutLink /
  conferenceData, organizer, description, attendees[] (responseStatus, self).
