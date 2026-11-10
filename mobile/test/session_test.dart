import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences_platform_interface/in_memory_shared_preferences_async.dart';
import 'package:shared_preferences_platform_interface/shared_preferences_async_platform_interface.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:widgetd_display/board_options.dart';
import 'package:widgetd_display/session.dart';

Session session(String id, {String url = 'http://h:8080/', bool autoLaunch = false}) =>
    Session(id: id, name: id, baseUrl: Uri.parse(url), autoLaunch: autoLaunch);

void main() {
  test('round-trips through JSON, options as protojson', () {
    final s = Session(
      id: 'a',
      name: 'Kitchen',
      baseUrl: Uri.parse('http://10.0.0.2:8080/'),
      options: BoardOptions(cols: 3, prefs: Preferences(temperature: TemperatureUnit.TEMPERATURE_UNIT_CELSIUS)),
      keepAwake: KeepAwake.window,
      awakeFrom: 22 * 60,
      awakeTo: 6 * 60,
      orientation: ScreenOrientation.landscape,
      autoLaunch: true,
    );
    final j = s.toJson();
    expect(j['options'], {'cols': 3, 'prefs': {'temperature': 'TEMPERATURE_UNIT_CELSIUS'}});
    final back = Session.fromJson(j);
    expect(back.toJson(), j);
    expect(back.boardUrl.toString(), 'http://10.0.0.2:8080/?o=CAMaAiAB');
  });

  test('loads JSON from a newer build: unknown fields and enum names fall back', () {
    final s = Session.fromJson({
      'id': 'a',
      'baseUrl': 'http://h/',
      'options': {'cols': 2, 'futureField': true},
      'keepAwake': 'someday',
      'orientation': 'sideways',
    });
    expect(s.options.cols, 2);
    expect(s.keepAwake, KeepAwake.always);
    expect(s.orientation, ScreenOrientation.auto);
  });

  group('awakeAt', () {
    DateTime at(int h, int m) => DateTime(2026, 11, 10, h, m);
    Session win(int from, int to) =>
        Session(id: 'a', name: '', baseUrl: Uri.parse('http://h/'), keepAwake: KeepAwake.window, awakeFrom: from, awakeTo: to);

    test('never and always', () {
      expect(awakeAt(session('a'), at(3, 0)), isTrue);
      expect(
          awakeAt(Session(id: 'a', name: '', baseUrl: Uri.parse('http://h/'), keepAwake: KeepAwake.never), at(12, 0)),
          isFalse);
    });
    test('a daytime window, end exclusive', () {
      final s = win(7 * 60, 22 * 60);
      expect(awakeAt(s, at(6, 59)), isFalse);
      expect(awakeAt(s, at(7, 0)), isTrue);
      expect(awakeAt(s, at(21, 59)), isTrue);
      expect(awakeAt(s, at(22, 0)), isFalse);
    });
    test('a window across midnight', () {
      final s = win(22 * 60, 6 * 60);
      expect(awakeAt(s, at(23, 0)), isTrue);
      expect(awakeAt(s, at(5, 59)), isTrue);
      expect(awakeAt(s, at(12, 0)), isFalse);
    });
    test('from == to is all day', () => expect(awakeAt(win(60, 60), at(12, 0)), isTrue));
  });

  group('parseBaseUrl', () {
    test('a bare host gets http:// and /', () {
      expect(parseBaseUrl(' 192.168.1.5:8080 ').url.toString(), 'http://192.168.1.5:8080/');
    });
    test('keeps https and a path', () {
      expect(parseBaseUrl('https://w.example.com/board').url.toString(), 'https://w.example.com/board');
    });
    test('refuses other schemes and empties', () {
      expect(parseBaseUrl('').error, isNotNull);
      expect(parseBaseUrl('ftp://h').error, isNotNull);
      expect(parseBaseUrl('http://').error, isNotNull);
    });
  });

  test('sameOrigin finds another session on the same server', () {
    final all = [session('a', url: 'http://h:8080/'), session('b', url: 'https://h/')];
    expect(sameOrigin(all, 'http://h:8080')?.id, 'a');
    expect(sameOrigin(all, 'http://h:8080', exceptId: 'a'), isNull);
    expect(sameOrigin(all, 'http://h:9090'), isNull);
  });

  group('SessionStore', () {
    setUp(() => SharedPreferencesAsyncPlatform.instance = InMemorySharedPreferencesAsync.empty());

    test('saves, replaces in place, deletes', () async {
      final store = SessionStore(SharedPreferencesAsync());
      await store.save(session('a'));
      await store.save(session('b', url: 'http://other/'));
      await store.save(session('a').copyWith(name: 'renamed'));
      var all = await store.load();
      expect(all.map((s) => s.name), ['renamed', 'b']);
      all = await store.delete('a');
      expect(all.map((s) => s.id), ['b']);
      expect((await store.load()).map((s) => s.id), ['b']);
    });

    test('at most one session auto-launches', () async {
      final store = SessionStore(SharedPreferencesAsync());
      await store.save(session('a', autoLaunch: true));
      final all = await store.save(session('b', url: 'http://other/', autoLaunch: true));
      expect({for (final s in all) s.id: s.autoLaunch}, {'a': false, 'b': true});
    });
  });
}
