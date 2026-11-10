import 'dart:convert';
import 'dart:io';

import 'package:fixnum/fixnum.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:widgetd_display/board_options.dart';
import 'package:widgetd_display/version.dart';

void main() {
  // The same {o, json} pairs the board's TS tests check, so the encodings
  // can't drift between the app and the board.
  final fixture = jsonDecode(File('../client/src/lib/options.fixture.json').readAsStringSync()) as List;

  group('options fixture', () {
    test('is not empty', () => expect(fixture, isNotEmpty));
    for (final f in fixture.cast<Map<String, Object?>>()) {
      final o = f['o'] as String;
      final json = f['json'];
      test('encodes ${jsonEncode(json)} as $o', () {
        expect(encodeBoardOptions(optionsFromJson(json)), o);
      });
      test('decodes $o', () {
        expect(decodeBoardOptions(o), optionsFromJson(json));
        expect(optionsToJson(decodeBoardOptions(o)), json);
      });
    }
  });

  test('an empty message adds no o', () {
    expect(encodeBoardOptions(BoardOptions()), '');
    expect(boardUri(Uri.parse('http://h:8080/'), BoardOptions()).toString(), 'http://h:8080/');
  });

  test('boardUri sets o, replacing one already there, and keeps other parameters', () {
    final opts = BoardOptions(cols: 3);
    expect(boardUri(Uri.parse('https://h/board?o=old&x=1'), opts).queryParameters, {'x': '1', 'o': 'CAM'});
  });

  test('explicit defaults encode like other encoders: left out', () {
    final o = BoardOptions(
      cols: 3,
      prefs: Preferences(hourCycle: HourCycle.HOUR_CYCLE_UNSPECIFIED, speed: SpeedUnit.SPEED_UNIT_UNSPECIFIED),
    );
    o.ensureStaleAfter()
      ..seconds = Int64(60)
      ..nanos = 0;
    expect(encodeBoardOptions(o), 'CAMSAgg8');
    expect(encodeBoardOptions(BoardOptions(prefs: Preferences(hourCycle: HourCycle.HOUR_CYCLE_UNSPECIFIED))), '');
    // `optional` fields keep presence even at zero: the board then refuses cols 0.
    expect(encodeBoardOptions(BoardOptions(cols: 0)), 'CAA');
  });

  test('a bad o throws FormatException', () {
    expect(() => decodeBoardOptions('_w'), throwsFormatException);
  });

  test('appVersion matches pubspec.yaml', () {
    final m = RegExp(r'^version:\s*([^+\s]+)', multiLine: true).firstMatch(File('pubspec.yaml').readAsStringSync());
    expect(appVersion, m?.group(1));
  });
}
