// Runs the display against a real widgetd server, on a simulator or device:
//
//   just run                                   # in another terminal
//   cd mobile && flutter test integration_test --dart-define=WIDGETD_URL=http://localhost:8080/
//
// (Android emulator: http://10.0.2.2:8080/.) Checks that the board page
// loads with its options, sees the app in its user agent, and has the
// window.open shim that sends the admin page to the system browser.

import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:webview_flutter/webview_flutter.dart';
import 'package:widgetd_display/board_options.dart';
import 'package:widgetd_display/session.dart';
import 'package:widgetd_display/ui/display.dart';
import 'package:widgetd_display/version.dart';

const _url = String.fromEnvironment('WIDGETD_URL', defaultValue: 'http://localhost:8080/');

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('shows the board with its options', (t) async {
    final session = Session(id: 'it', name: 'it', baseUrl: Uri.parse(_url), options: BoardOptions(cols: 3));
    late WebViewController web;
    await t.pumpWidget(MaterialApp(home: DisplayPage(session: session, onWebView: (c) => web = c)));

    // Wait for the board element to be defined and the shim installed.
    Future<Object?> js(String expr) async {
      final r = await web.runJavaScriptReturningResult('JSON.stringify($expr)');
      // iOS returns the string itself; Android a JSON-quoted string.
      var s = r.toString();
      if (s.startsWith('"')) s = jsonDecode(s) as String;
      return jsonDecode(s);
    }

    Object? state;
    for (var i = 0; i < 60; i++) {
      await t.pump(const Duration(milliseconds: 500));
      try {
        state = await js(
          '({search: location.search, ua: navigator.userAgent, '
          'board: !!(customElements.get("widget-board") && document.querySelector("widget-board")?.shadowRoot), '
          'shim: !!window.__widgetdApp})',
        );
        final m = state as Map;
        if (m['board'] == true && m['shim'] == true) break;
      } catch (_) {
        // the page isn't there yet
      }
    }
    final m = state as Map?;
    expect(m, isNotNull, reason: 'the page never answered: is the server up at $_url?');
    expect(m!['search'], '?o=CAM');
    expect(m['ua'], contains('widgetd-display/$appVersion'));
    expect(m['board'], isTrue);
    expect(m['shim'], isTrue);
  });
}
