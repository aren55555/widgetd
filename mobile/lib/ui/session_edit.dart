// Add or edit a session. Three kinds of setting:
//   - the server, which the board page comes from;
//   - board options, which go into the page URL (`?o=`): left blank, the
//     board decides or detects them;
//   - this device's settings, which only the app applies.

import 'package:flutter/material.dart';
import 'package:protobuf/protobuf.dart' show ProtobufEnum;

import '../board_options.dart';
import '../options_form.dart';
import '../session.dart';

sealed class EditResult {}

class Saved extends EditResult {
  Saved(this.session);
  final Session session;
}

class Deleted extends EditResult {
  Deleted(this.id);
  final String id;
}

class SessionEditPage extends StatefulWidget {
  const SessionEditPage({super.key, this.session, required this.others});

  /// The session to edit; null to add one.
  final Session? session;

  /// Every saved session, to refuse a second one for the same server.
  final List<Session> others;

  @override
  State<SessionEditPage> createState() => _SessionEditPageState();
}

class _SessionEditPageState extends State<SessionEditPage> {
  final _form = GlobalKey<FormState>();
  late final TextEditingController _name;
  late final TextEditingController _url;
  late final OptionsFields _opts;
  late KeepAwake _keepAwake;
  late TimeOfDay _awakeFrom;
  late TimeOfDay _awakeTo;
  late ScreenOrientation _orientation;
  late bool _autoLaunch;

  @override
  void initState() {
    super.initState();
    final s = widget.session;
    _name = TextEditingController(text: s?.name ?? '');
    _url = TextEditingController(text: s?.baseUrl.toString() ?? '');
    _opts = OptionsFields.from(s?.options ?? BoardOptions());
    _keepAwake = s?.keepAwake ?? KeepAwake.always;
    _awakeFrom = _time(s?.awakeFrom ?? 7 * 60);
    _awakeTo = _time(s?.awakeTo ?? 22 * 60);
    _orientation = s?.orientation ?? ScreenOrientation.auto;
    _autoLaunch = s?.autoLaunch ?? widget.others.isEmpty;
  }

  @override
  void dispose() {
    _name.dispose();
    _url.dispose();
    super.dispose();
  }

  static TimeOfDay _time(MinuteOfDay m) => TimeOfDay(hour: m ~/ 60, minute: m % 60);
  static MinuteOfDay _minutes(TimeOfDay t) => t.hour * 60 + t.minute;

  String? _urlError(String? text) {
    final r = parseBaseUrl(text ?? '');
    if (r.error != null) return r.error;
    final dup = sameOrigin(widget.others, r.url!.origin, exceptId: widget.session?.id);
    if (dup != null) {
      return 'Already added as "${dup.name.isEmpty ? dup.origin : dup.name}". '
          'A device can show one screen per server.';
    }
    return null;
  }

  void _save() {
    if (!(_form.currentState?.validate() ?? false)) return;
    final url = parseBaseUrl(_url.text).url!;
    Navigator.of(context).pop(Saved(Session(
      id: widget.session?.id ?? newSessionId(),
      name: _name.text.trim().isEmpty ? url.host : _name.text.trim(),
      baseUrl: url,
      options: _opts.toOptions(),
      keepAwake: _keepAwake,
      awakeFrom: _minutes(_awakeFrom),
      awakeTo: _minutes(_awakeTo),
      orientation: _orientation,
      autoLaunch: _autoLaunch,
    )));
  }

  Future<void> _delete() async {
    final s = widget.session!;
    final ok = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Remove this screen?'),
        content: const Text(
          'This device forgets it. The server still lists it as a paired device '
          'until an admin revokes it.',
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(context, false), child: const Text('Cancel')),
          TextButton(onPressed: () => Navigator.pop(context, true), child: const Text('Remove')),
        ],
      ),
    );
    if (ok == true && mounted) Navigator.of(context).pop(Deleted(s.id));
  }

  Future<void> _pickTime(bool from) async {
    final t = await showTimePicker(context: context, initialTime: from ? _awakeFrom : _awakeTo);
    if (t != null) setState(() => from ? _awakeFrom = t : _awakeTo = t);
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    Widget heading(String text, [String? hint]) => Padding(
          padding: const EdgeInsets.fromLTRB(0, 24, 0, 4),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(text, style: theme.textTheme.titleSmall?.copyWith(color: theme.colorScheme.primary)),
              if (hint != null) Text(hint, style: theme.textTheme.bodySmall),
            ],
          ),
        );

    return Scaffold(
      appBar: AppBar(
        title: Text(widget.session == null ? 'Add screen' : 'Edit screen'),
        actions: [
          if (widget.session != null)
            IconButton(icon: const Icon(Icons.delete_outline), tooltip: 'Remove', onPressed: _delete),
          TextButton(onPressed: _save, child: const Text('Save')),
        ],
      ),
      body: Form(
        key: _form,
        child: ListView(
          padding: const EdgeInsets.fromLTRB(16, 8, 16, 32),
          children: [
            TextFormField(
              controller: _url,
              decoration: const InputDecoration(labelText: 'Server', hintText: 'http://192.168.1.10:8080'),
              keyboardType: TextInputType.url,
              autocorrect: false,
              validator: _urlError,
            ),
            TextFormField(
              controller: _name,
              decoration: const InputDecoration(labelText: 'Name', hintText: 'Kitchen'),
            ),
            heading('Board', 'Blank: the board\'s default.'),
            Row(
              children: [
                Expanded(
                  child: TextFormField(
                    initialValue: _opts.cols,
                    decoration: const InputDecoration(labelText: 'Columns', hintText: '4'),
                    keyboardType: TextInputType.number,
                    validator: (v) => colsError(v ?? ''),
                    onChanged: (v) => _opts.cols = v,
                  ),
                ),
                const SizedBox(width: 16),
                Expanded(
                  child: TextFormField(
                    initialValue: _opts.staleAfterSeconds,
                    decoration: const InputDecoration(labelText: 'Offline badge after (s)', hintText: '30'),
                    keyboardType: TextInputType.number,
                    validator: (v) => staleAfterError(v ?? ''),
                    onChanged: (v) => _opts.staleAfterSeconds = v,
                  ),
                ),
              ],
            ),
            heading('Display preferences', 'Blank or "Detect": the board detects it from this device.'),
            TextFormField(
              initialValue: _opts.locale,
              decoration: const InputDecoration(labelText: 'Language & format', hintText: 'en-CA'),
              autocorrect: false,
              validator: (v) => localeError(v ?? ''),
              onChanged: (v) => _opts.locale = v,
            ),
            TextFormField(
              initialValue: _opts.timeZone,
              decoration: const InputDecoration(labelText: 'Time zone', hintText: 'America/Toronto'),
              autocorrect: false,
              validator: (v) => timeZoneError(v ?? ''),
              onChanged: (v) => _opts.timeZone = v,
            ),
            _enumField('Clock', _opts.hourCycle, HourCycle.values, _clockLabel, (v) => _opts.hourCycle = v),
            _enumField('Temperature', _opts.temperature, TemperatureUnit.values, _tempLabel,
                (v) => _opts.temperature = v),
            _enumField('Speed', _opts.speed, SpeedUnit.values, _speedLabel, (v) => _opts.speed = v),
            _enumField('Weight', _opts.mass, MassUnit.values, _massLabel, (v) => _opts.mass = v),
            _enumField('Distance', _opts.distance, DistanceUnit.values, _distanceLabel, (v) => _opts.distance = v),
            heading('This device'),
            DropdownButtonFormField<KeepAwake>(
              initialValue: _keepAwake,
              decoration: const InputDecoration(labelText: 'Keep the screen on'),
              items: const [
                DropdownMenuItem(value: KeepAwake.always, child: Text('Always')),
                DropdownMenuItem(value: KeepAwake.window, child: Text('During set hours')),
                DropdownMenuItem(value: KeepAwake.never, child: Text('Never (the system decides)')),
              ],
              onChanged: (v) => setState(() => _keepAwake = v ?? _keepAwake),
            ),
            if (_keepAwake == KeepAwake.window)
              Row(
                children: [
                  Expanded(
                    child: ListTile(
                      contentPadding: EdgeInsets.zero,
                      title: const Text('From'),
                      subtitle: Text(_awakeFrom.format(context)),
                      onTap: () => _pickTime(true),
                    ),
                  ),
                  Expanded(
                    child: ListTile(
                      contentPadding: EdgeInsets.zero,
                      title: const Text('Until'),
                      subtitle: Text(_awakeTo.format(context)),
                      onTap: () => _pickTime(false),
                    ),
                  ),
                ],
              ),
            DropdownButtonFormField<ScreenOrientation>(
              initialValue: _orientation,
              decoration: const InputDecoration(labelText: 'Orientation'),
              items: const [
                DropdownMenuItem(value: ScreenOrientation.auto, child: Text('Follow the device')),
                DropdownMenuItem(value: ScreenOrientation.landscape, child: Text('Landscape')),
                DropdownMenuItem(value: ScreenOrientation.portrait, child: Text('Portrait')),
              ],
              onChanged: (v) => setState(() => _orientation = v ?? _orientation),
            ),
            SwitchListTile(
              contentPadding: EdgeInsets.zero,
              title: const Text('Open when the app starts'),
              subtitle: const Text('Only one screen can.'),
              value: _autoLaunch,
              onChanged: (v) => setState(() => _autoLaunch = v),
            ),
          ],
        ),
      ),
    );
  }

  /// A dropdown over a proto enum, whose UNSPECIFIED value reads "Detect".
  Widget _enumField<E extends ProtobufEnum>(
    String label,
    E value,
    List<E> values,
    String Function(E) name,
    void Function(E) set,
  ) =>
      DropdownButtonFormField<E>(
        initialValue: value,
        decoration: InputDecoration(labelText: label),
        items: [
          for (final v in values)
            DropdownMenuItem(value: v, child: Text(v.value == 0 ? 'Detect' : name(v))),
        ],
        onChanged: (v) => setState(() => set(v ?? value)),
      );
}

String _clockLabel(HourCycle h) => switch (h) {
      HourCycle.HOUR_CYCLE_H12 => '12-hour',
      HourCycle.HOUR_CYCLE_H24 => '24-hour',
      _ => h.name,
    };

String _tempLabel(TemperatureUnit t) => switch (t) {
      TemperatureUnit.TEMPERATURE_UNIT_CELSIUS => 'Celsius (°C)',
      TemperatureUnit.TEMPERATURE_UNIT_FAHRENHEIT => 'Fahrenheit (°F)',
      _ => t.name,
    };

String _speedLabel(SpeedUnit s) => switch (s) {
      SpeedUnit.SPEED_UNIT_KPH => 'km/h',
      SpeedUnit.SPEED_UNIT_MPH => 'mph',
      SpeedUnit.SPEED_UNIT_MPS => 'm/s',
      _ => s.name,
    };

String _massLabel(MassUnit m) => switch (m) {
      MassUnit.MASS_UNIT_KG => 'kg',
      MassUnit.MASS_UNIT_LB => 'lb',
      _ => m.name,
    };

String _distanceLabel(DistanceUnit d) => switch (d) {
      DistanceUnit.DISTANCE_UNIT_KM => 'km',
      DistanceUnit.DISTANCE_UNIT_MI => 'mi',
      _ => d.name,
    };
