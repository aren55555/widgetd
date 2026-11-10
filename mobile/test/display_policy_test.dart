import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:widgetd_display/display_policy.dart';
import 'package:widgetd_display/session.dart';
import 'package:widgetd_display/version.dart';

void main() {
  final s = Session(id: 'a', name: 'a', baseUrl: Uri.parse('http://10.0.0.2:8080/'));

  test('withAppTag appends once, in the form the board parses', () {
    const ua = 'Mozilla/5.0 (Linux; Android 14) Mobile Safari/537.36';
    expect(withAppTag(ua), '$ua widgetd-display/$appVersion');
    expect(withAppTag(withAppTag(ua)), withAppTag(ua));
    expect(withAppTag(null), 'widgetd-display/$appVersion');
    // The board's appVersion(): /\bwidgetd-display\/(\S+)/
    expect(RegExp(r'\bwidgetd-display/(\S+)').firstMatch(withAppTag(ua))?.group(1), appVersion);
  });

  test('off-origin http(s) pages open externally; the board origin and other schemes stay', () {
    expect(opensExternally(Uri.parse('http://10.0.0.2:8080/widgetd/frame/x/y'), s), isFalse);
    expect(opensExternally(Uri.parse('https://10.0.0.2:8080/'), s), isTrue); // another scheme is another origin
    expect(opensExternally(Uri.parse('https://github.com/login/device'), s), isTrue);
    expect(opensExternally(Uri.parse('about:blank'), s), isFalse);
  });

  test('externalUrl accepts http(s) only', () {
    expect(externalUrl('https://h/admin')?.host, 'h');
    expect(externalUrl('javascript:alert(1)'), isNull);
    expect(externalUrl('intent://x'), isNull);
    expect(externalUrl('not a url'), isNull);
  });

  test('orientations', () {
    expect(orientationsFor(ScreenOrientation.auto), isEmpty);
    expect(orientationsFor(ScreenOrientation.landscape), contains(DeviceOrientation.landscapeLeft));
    expect(orientationsFor(ScreenOrientation.portrait), isNot(contains(DeviceOrientation.landscapeLeft)));
  });

  test('the window.open shim posts to the app channel', () {
    expect(windowOpenShim, contains('$appChannel.postMessage'));
  });
}
