// widgetd display: shows a widgetd board full screen on a phone or tablet.
// See docs/mobile-app.md.

import 'package:flutter/material.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'session.dart';
import 'ui/session_list.dart';

void main() {
  WidgetsFlutterBinding.ensureInitialized();
  runApp(DisplayApp(store: SessionStore(SharedPreferencesAsync())));
}

class DisplayApp extends StatelessWidget {
  const DisplayApp({super.key, required this.store});

  final SessionStore store;

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'widgetd',
      theme: ThemeData(colorSchemeSeed: const Color(0xFF6366F1), brightness: Brightness.dark),
      home: SessionListPage(store: store),
    );
  }
}
