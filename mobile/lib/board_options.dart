// A session's BoardOptions as the board page reads them: one query parameter,
// `?o=`, holding the unpadded base64url of the binary message. Fields left
// unset aren't written, so an empty message adds no `o` at all. The encoding
// is pinned by client/src/lib/options.fixture.json, which the TS tests and
// this app's tests both check.

import 'dart:convert';

import 'gen/widgetd/v1/board.pb.dart';
import 'gen/widgetd/v1/prefs.pb.dart';

export 'gen/widgetd/v1/board.pb.dart';
export 'gen/widgetd/v1/prefs.pb.dart';
export 'gen/widgetd/v1/prefs.pbenum.dart';

/// [options] as protoc and protobuf-es would write them. Dart protobuf
/// writes an implicit-presence field that was set to its default (protojson
/// parsing sets Duration.nanos = 0, a form sets an enum to UNSPECIFIED);
/// other encoders leave such fields out. Clearing them keeps the bytes, and
/// so `o`, identical to the board's. Fields declared `optional` (cols,
/// locale, time_zone) keep their presence.
BoardOptions canonicalOptions(BoardOptions options) {
  final o = options.deepCopy();
  if (o.hasStaleAfter()) {
    final d = o.staleAfter;
    if (d.hasSeconds() && d.seconds == 0) d.clearSeconds();
    if (d.hasNanos() && d.nanos == 0) d.clearNanos();
  }
  if (o.hasPrefs()) {
    final p = o.prefs;
    if (p.hasHourCycle() && p.hourCycle.value == 0) p.clearHourCycle();
    if (p.hasTemperature() && p.temperature.value == 0) p.clearTemperature();
    if (p.hasSpeed() && p.speed.value == 0) p.clearSpeed();
    if (p.hasMass() && p.mass.value == 0) p.clearMass();
    if (p.hasDistance() && p.distance.value == 0) p.clearDistance();
    if (p == Preferences()) o.clearPrefs();
  }
  return o;
}

/// The `o` value for [options]; empty when nothing is set.
String encodeBoardOptions(BoardOptions options) =>
    base64Url.encode(canonicalOptions(options).writeToBuffer()).replaceAll('=', '');

/// The options in an `o` value. Throws [FormatException] if it doesn't decode.
BoardOptions decodeBoardOptions(String o) {
  final padded = o.padRight((o.length + 3) ~/ 4 * 4, '=');
  try {
    return BoardOptions.fromBuffer(base64Url.decode(padded));
  } on FormatException {
    rethrow;
  } catch (e) {
    throw FormatException('o does not decode: $e');
  }
}

/// The board page for [base] with [options]: `?o=` replaces any `o` already
/// in the URL, and is left out when nothing is set.
Uri boardUri(Uri base, BoardOptions options) {
  final query = Map<String, String>.of(base.queryParameters)..remove('o');
  final o = encodeBoardOptions(options);
  if (o.isNotEmpty) query['o'] = o;
  return base.replace(queryParameters: query.isEmpty ? null : query);
}

/// The options as protojson, the form a session stores them in.
Object? optionsToJson(BoardOptions options) => canonicalOptions(options).toProto3Json();

/// Options read back from protojson. Fields this build doesn't know are
/// skipped rather than refused, so a newer app's sessions still load.
BoardOptions optionsFromJson(Object? json) {
  final o = BoardOptions();
  if (json != null) o.mergeFromProto3Json(json, ignoreUnknownFields: true);
  return canonicalOptions(o);
}
