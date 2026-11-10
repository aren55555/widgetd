// The session form's board settings: validation that mirrors board.proto's
// rules (so the board never ignores what the form saved), and building
// BoardOptions from the fields. Blank means unset: the board decides.

import 'package:fixnum/fixnum.dart';
import 'package:protobuf/well_known_types/google/protobuf/duration.pb.dart' as pb;

import 'board_options.dart';

String? colsError(String text) {
  if (text.trim().isEmpty) return null;
  final n = int.tryParse(text.trim());
  return n == null || n < 1 || n > 12 ? 'A whole number from 1 to 12' : null;
}

String? staleAfterError(String text) {
  if (text.trim().isEmpty) return null;
  final n = int.tryParse(text.trim());
  return n == null || n < 5 ? 'At least 5 seconds' : null;
}

final _locale = RegExp(r'^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*$');

/// A BCP 47 tag, roughly: the board checks it properly, and detects instead
/// if it doesn't like it.
String? localeError(String text) =>
    text.trim().isEmpty || _locale.hasMatch(text.trim()) ? null : 'A language tag, e.g. en-CA';

final _zone = RegExp(r'^[A-Za-z][A-Za-z0-9_+\-]*(/[A-Za-z0-9_+\-]+)*$');

String? timeZoneError(String text) =>
    text.trim().isEmpty || _zone.hasMatch(text.trim()) ? null : 'An IANA zone, e.g. America/Toronto';

/// What the form's board fields hold.
class OptionsFields {
  OptionsFields({
    this.cols = '',
    this.staleAfterSeconds = '',
    this.locale = '',
    this.timeZone = '',
    this.hourCycle = HourCycle.HOUR_CYCLE_UNSPECIFIED,
    this.temperature = TemperatureUnit.TEMPERATURE_UNIT_UNSPECIFIED,
    this.speed = SpeedUnit.SPEED_UNIT_UNSPECIFIED,
    this.mass = MassUnit.MASS_UNIT_UNSPECIFIED,
    this.distance = DistanceUnit.DISTANCE_UNIT_UNSPECIFIED,
  });

  factory OptionsFields.from(BoardOptions o) {
    final p = o.prefs;
    return OptionsFields(
      cols: o.hasCols() ? '${o.cols}' : '',
      staleAfterSeconds: o.hasStaleAfter() ? '${o.staleAfter.seconds}' : '',
      locale: p.hasLocale() ? p.locale : '',
      timeZone: p.hasTimeZone() ? p.timeZone : '',
      hourCycle: p.hourCycle,
      temperature: p.temperature,
      speed: p.speed,
      mass: p.mass,
      distance: p.distance,
    );
  }

  String cols;
  String staleAfterSeconds;
  String locale;
  String timeZone;
  HourCycle hourCycle;
  TemperatureUnit temperature;
  SpeedUnit speed;
  MassUnit mass;
  DistanceUnit distance;

  /// The options these fields describe. Call only once the validators pass.
  BoardOptions toOptions() {
    final o = BoardOptions();
    if (cols.trim().isNotEmpty) o.cols = int.parse(cols.trim());
    if (staleAfterSeconds.trim().isNotEmpty) {
      o.staleAfter = pb.Duration(seconds: Int64(int.parse(staleAfterSeconds.trim())));
    }
    final p = Preferences(hourCycle: hourCycle, temperature: temperature, speed: speed, mass: mass, distance: distance);
    if (locale.trim().isNotEmpty) p.locale = locale.trim();
    if (timeZone.trim().isNotEmpty) p.timeZone = timeZone.trim();
    o.prefs = p;
    return canonicalOptions(o);
  }
}
