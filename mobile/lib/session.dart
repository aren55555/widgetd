// A session is one widgetd screen this device can show: a server URL, the
// BoardOptions passed to its board page, and the settings only the native
// shell can apply (keep-awake, orientation, auto-launch). Sessions are kept
// as JSON in shared_preferences.
//
// The screen's pairing credential is the server's HttpOnly cookie in the
// WebView's cookie jar, which is per origin, so there is at most one session
// per server origin.

import 'dart:convert';

import 'package:shared_preferences/shared_preferences.dart';

import 'board_options.dart';

enum KeepAwake { never, always, window }

enum ScreenOrientation { auto, portrait, landscape }

/// A time of day, in minutes since midnight.
typedef MinuteOfDay = int;

class Session {
  Session({
    required this.id,
    required this.name,
    required this.baseUrl,
    BoardOptions? options,
    this.keepAwake = KeepAwake.always,
    this.awakeFrom = 7 * 60,
    this.awakeTo = 22 * 60,
    this.orientation = ScreenOrientation.auto,
    this.autoLaunch = false,
  }) : options = canonicalOptions(options ?? BoardOptions());

  final String id;
  final String name;

  /// The board page, e.g. `http://192.168.1.10:8080/`.
  final Uri baseUrl;
  final BoardOptions options;
  final KeepAwake keepAwake;

  /// The keep-awake window for [KeepAwake.window]. It may cross midnight
  /// (from 22:00 to 06:00).
  final MinuteOfDay awakeFrom;
  final MinuteOfDay awakeTo;
  final ScreenOrientation orientation;

  /// Open this session when the app starts. At most one session has it.
  final bool autoLaunch;

  /// The page the display loads: [baseUrl] with the options in `?o=`.
  Uri get boardUrl => boardUri(baseUrl, options);

  /// `scheme://host:port`, the pairing cookie's scope.
  String get origin => baseUrl.origin;

  Session copyWith({String? name, bool? autoLaunch}) => Session(
        id: id,
        name: name ?? this.name,
        baseUrl: baseUrl,
        options: options,
        keepAwake: keepAwake,
        awakeFrom: awakeFrom,
        awakeTo: awakeTo,
        orientation: orientation,
        autoLaunch: autoLaunch ?? this.autoLaunch,
      );

  Map<String, Object?> toJson() => {
        'id': id,
        'name': name,
        'baseUrl': baseUrl.toString(),
        'options': optionsToJson(options),
        'keepAwake': keepAwake.name,
        'awakeFrom': awakeFrom,
        'awakeTo': awakeTo,
        'orientation': orientation.name,
        'autoLaunch': autoLaunch,
      };

  factory Session.fromJson(Map<String, Object?> j) => Session(
        id: j['id'] as String,
        name: j['name'] as String? ?? '',
        baseUrl: Uri.parse(j['baseUrl'] as String),
        options: optionsFromJson(j['options']),
        keepAwake: _enumByName(KeepAwake.values, j['keepAwake'], KeepAwake.always),
        awakeFrom: (j['awakeFrom'] as num?)?.toInt() ?? 7 * 60,
        awakeTo: (j['awakeTo'] as num?)?.toInt() ?? 22 * 60,
        orientation: _enumByName(ScreenOrientation.values, j['orientation'], ScreenOrientation.auto),
        autoLaunch: j['autoLaunch'] == true,
      );
}

T _enumByName<T extends Enum>(List<T> values, Object? name, T fallback) =>
    values.where((v) => v.name == name).firstOrNull ?? fallback;

/// Whether the screen should be kept on at [now].
bool awakeAt(Session s, DateTime now) {
  switch (s.keepAwake) {
    case KeepAwake.never:
      return false;
    case KeepAwake.always:
      return true;
    case KeepAwake.window:
      final m = now.hour * 60 + now.minute;
      if (s.awakeFrom == s.awakeTo) return true; // a full day
      return s.awakeFrom < s.awakeTo
          ? m >= s.awakeFrom && m < s.awakeTo
          : m >= s.awakeFrom || m < s.awakeTo; // crosses midnight
  }
}

/// The board page a user typed, made into a URL, or why it can't be one.
/// A bare host gets `http://` (a home server on the local network is the
/// common case) and an empty path gets `/`.
({Uri? url, String? error}) parseBaseUrl(String input) {
  var text = input.trim();
  if (text.isEmpty) return (url: null, error: 'Enter the server address');
  if (!text.contains('://')) text = 'http://$text';
  final u = Uri.tryParse(text);
  if (u == null || !(u.scheme == 'http' || u.scheme == 'https') || u.host.isEmpty) {
    return (url: null, error: 'Enter an http:// or https:// address');
  }
  return (url: u.path.isEmpty ? u.replace(path: '/') : u, error: null);
}

/// The session (other than [exceptId]) already showing a screen of [origin].
Session? sameOrigin(Iterable<Session> sessions, String origin, {String? exceptId}) =>
    sessions.where((s) => s.id != exceptId && s.origin == origin).firstOrNull;

/// The sessions, kept as one JSON list in shared_preferences.
class SessionStore {
  SessionStore(this._prefs);

  static const _key = 'sessions';
  final SharedPreferencesAsync _prefs;

  Future<List<Session>> load() async {
    final raw = await _prefs.getString(_key);
    if (raw == null) return [];
    try {
      return [
        for (final j in jsonDecode(raw) as List) Session.fromJson((j as Map).cast<String, Object?>()),
      ];
    } catch (_) {
      return []; // unreadable: start over rather than crash at launch
    }
  }

  Future<void> _write(List<Session> sessions) =>
      _prefs.setString(_key, jsonEncode([for (final s in sessions) s.toJson()]));

  /// Add or replace [s]. If it auto-launches, no other session does.
  Future<List<Session>> save(Session s) async {
    final all = await load();
    final out = <Session>[
      for (final o in all)
        if (o.id != s.id) s.autoLaunch && o.autoLaunch ? o.copyWith(autoLaunch: false) : o,
    ];
    final at = all.indexWhere((o) => o.id == s.id);
    out.insert(at < 0 ? out.length : at, s);
    await _write(out);
    return out;
  }

  /// Forget [id] on this device. The server keeps the screen's device
  /// session until an admin revokes it.
  Future<List<Session>> delete(String id) async {
    final out = [for (final s in await load()) if (s.id != id) s];
    await _write(out);
    return out;
  }
}

/// A new session id.
String newSessionId() => DateTime.now().microsecondsSinceEpoch.toRadixString(36);
