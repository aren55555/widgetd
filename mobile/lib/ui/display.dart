// A session's board, full screen in a WebView. Everything about the board
// (pairing, cards, offline state) is the web page's; this applies only what
// a native shell must: immersive mode, orientation, keeping the screen on,
// sending off-origin pages to the system browser, and a way back out.

import 'dart:async';

import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:url_launcher/url_launcher.dart';
import 'package:wakelock_plus/wakelock_plus.dart';
import 'package:webview_flutter/webview_flutter.dart';

import '../display_policy.dart';
import '../session.dart';

class DisplayPage extends StatefulWidget {
  const DisplayPage({super.key, required this.session, this.onWebView});

  final Session session;

  /// Gets the WebView's controller once it exists (the integration test
  /// reads the page through it).
  @visibleForTesting
  final void Function(WebViewController)? onWebView;

  @override
  State<DisplayPage> createState() => _DisplayPageState();
}

class _DisplayPageState extends State<DisplayPage> with WidgetsBindingObserver {
  late final WebViewController _web;
  Timer? _awakeTimer;

  Session get _session => widget.session;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    SystemChrome.setEnabledSystemUIMode(SystemUiMode.immersiveSticky);
    SystemChrome.setPreferredOrientations(orientationsFor(_session.orientation));
    _applyAwake();
    // A keep-awake window opens and closes while the board is showing.
    _awakeTimer = Timer.periodic(const Duration(seconds: 30), (_) => _applyAwake());

    _web = WebViewController()
      ..setJavaScriptMode(JavaScriptMode.unrestricted)
      ..setBackgroundColor(Colors.black)
      ..addJavaScriptChannel(appChannel, onMessageReceived: (m) => _openExternally(m.message))
      ..setNavigationDelegate(NavigationDelegate(
        onNavigationRequest: (r) {
          final to = Uri.tryParse(r.url);
          if (r.isMainFrame && to != null && opensExternally(to, _session)) {
            _openExternally(r.url);
            return NavigationDecision.prevent;
          }
          return NavigationDecision.navigate;
        },
        onPageFinished: (_) => _web.runJavaScript(windowOpenShim),
        onWebResourceError: (e) {
          // iOS reports a crashed web content process this way: reload, and
          // the board draws from its cache and resumes.
          if (e.errorType == WebResourceErrorType.webContentProcessTerminated) _web.reload();
        },
      ));
    _load();
    widget.onWebView?.call(_web);
  }

  Future<void> _load() async {
    await _web.setUserAgent(withAppTag(await _web.getUserAgent()));
    await _web.loadRequest(_session.boardUrl);
  }

  void _applyAwake() => WakelockPlus.toggle(enable: awakeAt(_session, DateTime.now()));

  Future<void> _openExternally(String raw) async {
    final u = externalUrl(raw);
    if (u != null) await launchUrl(u, mode: LaunchMode.externalApplication);
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed) {
      // The OS may have restored the system bars or released the wakelock.
      SystemChrome.setEnabledSystemUIMode(SystemUiMode.immersiveSticky);
      _applyAwake();
    }
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _awakeTimer?.cancel();
    WakelockPlus.disable();
    SystemChrome.setEnabledSystemUIMode(SystemUiMode.edgeToEdge);
    SystemChrome.setPreferredOrientations(const []);
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.black,
      body: Stack(
        children: [
          WebViewWidget(controller: _web),
          // Back to the session list: a 2-second press in the top-left corner
          // (Android's back button also works).
          Positioned(
            left: 0,
            top: 0,
            width: 48,
            height: 48,
            child: RawGestureDetector(
              behavior: HitTestBehavior.opaque,
              gestures: {
                LongPressGestureRecognizer: GestureRecognizerFactoryWithHandlers<LongPressGestureRecognizer>(
                  () => LongPressGestureRecognizer(duration: const Duration(seconds: 2)),
                  (r) => r.onLongPress = () => Navigator.of(context).maybePop(),
                ),
              },
              child: const SizedBox.expand(),
            ),
          ),
        ],
      ),
    );
  }
}
