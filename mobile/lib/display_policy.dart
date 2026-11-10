// The display's rules, apart from the widgets that apply them, so they can
// be tested without a WebView.

import 'package:flutter/services.dart';

import 'session.dart';
import 'version.dart';

/// The WebView's user agent with the app's tag appended. The board's settings
/// modal reads `widgetd-display/<version>` from it to show the app version.
String withAppTag(String? userAgent) {
  const tag = 'widgetd-display/$appVersion';
  final ua = (userAgent ?? '').trim();
  if (ua.contains(tag)) return ua;
  return ua.isEmpty ? tag : '$ua $tag';
}

/// Whether a top-level navigation to [to] should leave the app and open in
/// the system browser: any http(s) page off the session's origin. The admin
/// page (passkeys) and providers' sign-in pages belong in a real browser.
bool opensExternally(Uri to, Session s) =>
    (to.scheme == 'http' || to.scheme == 'https') && to.origin != s.origin;

/// A URL the page asked to open (window.open, via the app's JS channel), if
/// it is one the system browser should get: http(s) only.
Uri? externalUrl(String raw) {
  final u = Uri.tryParse(raw);
  return u != null && (u.scheme == 'http' || u.scheme == 'https') && u.host.isNotEmpty ? u : null;
}

List<DeviceOrientation> orientationsFor(ScreenOrientation o) => switch (o) {
      ScreenOrientation.auto => const [],
      ScreenOrientation.portrait => const [DeviceOrientation.portraitUp, DeviceOrientation.portraitDown],
      ScreenOrientation.landscape => const [DeviceOrientation.landscapeLeft, DeviceOrientation.landscapeRight],
    };

/// The JavaScript channel the board page reaches the app through.
const appChannel = 'WidgetdApp';

/// Installed into each loaded page: neither platform WebView opens a new
/// window for window.open, so hand the URL to the app, which opens it in the
/// system browser. The board's settings modal opens the admin page this way.
const windowOpenShim = '''
(function () {
  if (window.__widgetdApp) return;
  window.__widgetdApp = true;
  window.open = function (url) {
    try { $appChannel.postMessage(new URL(String(url), location.href).href); } catch (e) {}
    return null;
  };
})();
''';
