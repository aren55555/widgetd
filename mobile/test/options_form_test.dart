import 'package:flutter_test/flutter_test.dart';
import 'package:widgetd_display/board_options.dart';
import 'package:widgetd_display/options_form.dart';

void main() {
  test('validators mirror board.proto: cols 1..12, stale_after >= 5 s, blank is fine', () {
    for (final ok in ['', ' ', '1', '12']) {
      expect(colsError(ok), isNull, reason: ok);
    }
    for (final bad in ['0', '13', 'x', '1.5']) {
      expect(colsError(bad), isNotNull, reason: bad);
    }
    expect(staleAfterError(''), isNull);
    expect(staleAfterError('5'), isNull);
    expect(staleAfterError('4'), isNotNull);
    expect(localeError('en-CA'), isNull);
    expect(localeError('en-CA-u-ms-metric'), isNull);
    expect(localeError('en_CA'), isNotNull);
    expect(timeZoneError('America/Argentina/Buenos_Aires'), isNull);
    expect(timeZoneError('UTC'), isNull);
    expect(timeZoneError('Mars Time'), isNotNull);
  });

  test('blank fields leave options unset', () {
    expect(encodeBoardOptions(OptionsFields().toOptions()), '');
  });

  test('fields build the fixture encoding', () {
    final f = OptionsFields(cols: '3', staleAfterSeconds: '60');
    expect(encodeBoardOptions(f.toOptions()), 'CAMSAgg8');
  });

  test('round-trips through the form fields', () {
    final o = OptionsFields(
      cols: '6',
      locale: 'en-GB',
      timeZone: 'Europe/London',
      speed: SpeedUnit.SPEED_UNIT_MPS,
    ).toOptions();
    final back = OptionsFields.from(o);
    expect([back.cols, back.staleAfterSeconds, back.locale, back.timeZone, back.speed],
        ['6', '', 'en-GB', 'Europe/London', SpeedUnit.SPEED_UNIT_MPS]);
    expect(back.toOptions(), o);
  });
}
