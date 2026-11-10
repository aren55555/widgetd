// The app's home: the sessions on this device. Tap one to show it; the
// session marked "open at launch" opens by itself when the app starts.

import 'package:flutter/material.dart';

import '../session.dart';
import 'display.dart';
import 'session_edit.dart';

class SessionListPage extends StatefulWidget {
  const SessionListPage({super.key, required this.store, this.autoLaunch = true});

  final SessionStore store;

  /// Open the auto-launch session once the list has loaded.
  final bool autoLaunch;

  @override
  State<SessionListPage> createState() => _SessionListPageState();
}

class _SessionListPageState extends State<SessionListPage> {
  List<Session>? _sessions;

  @override
  void initState() {
    super.initState();
    widget.store.load().then((all) {
      if (!mounted) return;
      setState(() => _sessions = all);
      final launch = all.where((s) => s.autoLaunch).firstOrNull;
      if (widget.autoLaunch && launch != null) _open(launch);
    });
  }

  void _open(Session s) =>
      Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => DisplayPage(session: s)));

  Future<void> _edit([Session? s]) async {
    final result = await Navigator.of(context).push(
      MaterialPageRoute<EditResult>(builder: (_) => SessionEditPage(session: s, others: _sessions ?? const [])),
    );
    if (result == null) return;
    final all = switch (result) {
      Saved(:final session) => await widget.store.save(session),
      Deleted(:final id) => await widget.store.delete(id),
    };
    if (mounted) setState(() => _sessions = all);
  }

  @override
  Widget build(BuildContext context) {
    final sessions = _sessions;
    return Scaffold(
      appBar: AppBar(title: const Text('widgetd')),
      floatingActionButton: FloatingActionButton.extended(
        onPressed: () => _edit(),
        icon: const Icon(Icons.add),
        label: const Text('Add screen'),
      ),
      body: switch (sessions) {
        null => const Center(child: CircularProgressIndicator()),
        [] => const Center(
            child: Padding(
              padding: EdgeInsets.all(32),
              child: Text(
                'Add a screen: the address of your widgetd server. The first time it opens, '
                'it shows a code to approve from your phone.',
                textAlign: TextAlign.center,
              ),
            ),
          ),
        _ => ListView(
            children: [
              for (final s in sessions)
                ListTile(
                  leading: const Icon(Icons.dashboard_outlined),
                  title: Text(s.name.isEmpty ? s.baseUrl.host : s.name),
                  subtitle: Text([s.origin, if (s.autoLaunch) 'opens at launch'].join(' · ')),
                  onTap: () => _open(s),
                  trailing: IconButton(
                    icon: const Icon(Icons.edit_outlined),
                    tooltip: 'Edit',
                    onPressed: () => _edit(s),
                  ),
                ),
            ],
          ),
      },
    );
  }
}
