import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:shared_preferences_platform_interface/in_memory_shared_preferences_async.dart';
import 'package:shared_preferences_platform_interface/shared_preferences_async_platform_interface.dart';
import 'package:widgetd_display/session.dart';
import 'package:widgetd_display/ui/session_edit.dart';
import 'package:widgetd_display/ui/session_list.dart';

void main() {
  final existing = Session(id: 'a', name: 'Kitchen', baseUrl: Uri.parse('http://10.0.0.2:8080/'));

  Future<EditResult?> edit(WidgetTester t, {Session? session, List<Session> others = const []}) async {
    EditResult? result;
    await t.pumpWidget(MaterialApp(
      home: Builder(
        builder: (context) => TextButton(
          onPressed: () async => result = await Navigator.of(context).push(
            MaterialPageRoute<EditResult>(builder: (_) => SessionEditPage(session: session, others: others)),
          ),
          child: const Text('open'),
        ),
      ),
    ));
    await t.tap(find.text('open'));
    await t.pumpAndSettle();
    return result;
  }

  testWidgets('adds a session with board options from the form', (t) async {
    await edit(t);
    await t.enterText(find.widgetWithText(TextFormField, 'Server'), '10.0.0.9:8080');
    await t.enterText(find.widgetWithText(TextFormField, 'Columns'), '3');
    await t.enterText(find.widgetWithText(TextFormField, 'Offline badge after (s)'), '60');
    await t.tap(find.text('Save'));
    await t.pumpAndSettle();
    expect(find.text('Save'), findsNothing); // popped
  });

  testWidgets('refuses a second session for the same server', (t) async {
    await edit(t, others: [existing]);
    await t.enterText(find.widgetWithText(TextFormField, 'Server'), 'http://10.0.0.2:8080/other');
    await t.tap(find.text('Save'));
    await t.pumpAndSettle();
    expect(find.textContaining('Already added as "Kitchen"'), findsOneWidget);
  });

  testWidgets('refuses columns the board would ignore', (t) async {
    await edit(t);
    await t.enterText(find.widgetWithText(TextFormField, 'Server'), '10.0.0.9');
    await t.enterText(find.widgetWithText(TextFormField, 'Columns'), '13');
    await t.tap(find.text('Save'));
    await t.pumpAndSettle();
    expect(find.text('A whole number from 1 to 12'), findsOneWidget);
  });

  testWidgets('the list shows saved sessions, and adds through the form', (t) async {
    SharedPreferencesAsyncPlatform.instance = InMemorySharedPreferencesAsync.empty();
    final store = SessionStore(SharedPreferencesAsync());
    await store.save(existing);
    await t.pumpWidget(MaterialApp(home: SessionListPage(store: store, autoLaunch: false)));
    await t.pumpAndSettle();
    expect(find.text('Kitchen'), findsOneWidget);

    await t.tap(find.text('Add screen'));
    await t.pumpAndSettle();
    await t.enterText(find.widgetWithText(TextFormField, 'Server'), '10.0.0.9:8080');
    await t.enterText(find.widgetWithText(TextFormField, 'Name'), 'Hall');
    await t.tap(find.text('Save'));
    await t.pumpAndSettle();

    expect(find.text('Hall'), findsOneWidget);
    final saved = await store.load();
    expect(saved.map((s) => s.name), ['Kitchen', 'Hall']);
    expect(saved.last.baseUrl.toString(), 'http://10.0.0.9:8080/');
    expect(saved.last.autoLaunch, isFalse); // only the first screen defaults to it
  });
}
