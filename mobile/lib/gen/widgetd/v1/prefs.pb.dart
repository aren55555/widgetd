// This is a generated file - do not edit.
//
// Generated from widgetd/v1/prefs.proto.

// @dart = 3.3

// ignore_for_file: annotate_overrides, camel_case_types, comment_references
// ignore_for_file: constant_identifier_names
// ignore_for_file: curly_braces_in_flow_control_structures
// ignore_for_file: deprecated_member_use_from_same_package, library_prefixes
// ignore_for_file: non_constant_identifier_names, prefer_relative_imports

import 'dart:core' as $core;

import 'package:protobuf/protobuf.dart' as $pb;

import 'prefs.pbenum.dart';

export 'package:protobuf/protobuf.dart' show GeneratedMessageGenericExtensions;

export 'prefs.pbenum.dart';

/// Preferences says how a screen shows times and quantities. It is part of a
/// screen's BoardOptions, and the board hands the resolved value to every
/// card as ctx.prefs. It is its own file because it is also generated into
/// the web SDK.
///
/// Unset or *_UNSPECIFIED means: detect on the display.
class Preferences extends $pb.GeneratedMessage {
  factory Preferences({
    $core.String? locale,
    $core.String? timeZone,
    HourCycle? hourCycle,
    TemperatureUnit? temperature,
    SpeedUnit? speed,
    MassUnit? mass,
    DistanceUnit? distance,
  }) {
    final result = Preferences._();
    if (locale != null) result.locale = locale;
    if (timeZone != null) result.timeZone = timeZone;
    if (hourCycle != null) result.hourCycle = hourCycle;
    if (temperature != null) result.temperature = temperature;
    if (speed != null) result.speed = speed;
    if (mass != null) result.mass = mass;
    if (distance != null) result.distance = distance;
    return result;
  }

  Preferences._();

  factory Preferences.fromBuffer($core.List<$core.int> data,
          [$pb.ExtensionRegistry registry = $pb.ExtensionRegistry.EMPTY]) =>
      Preferences()..mergeFromBuffer(data, registry);
  factory Preferences.fromJson($core.String json,
          [$pb.ExtensionRegistry registry = $pb.ExtensionRegistry.EMPTY]) =>
      Preferences()..mergeFromJson(json, registry);

  static final $pb.BuilderInfo _i = $pb.BuilderInfo(
      _omitMessageNames ? '' : 'Preferences',
      package: const $pb.PackageName(_omitMessageNames ? '' : 'widgetd.v1'),
      createEmptyInstance: Preferences.$_createMessage)
    ..aOS(1, _omitFieldNames ? '' : 'locale')
    ..aOS(2, _omitFieldNames ? '' : 'timeZone')
    ..aE<HourCycle>(3, _omitFieldNames ? '' : 'hourCycle',
        enumValues: HourCycle.values)
    ..aE<TemperatureUnit>(4, _omitFieldNames ? '' : 'temperature',
        enumValues: TemperatureUnit.values)
    ..aE<SpeedUnit>(5, _omitFieldNames ? '' : 'speed',
        enumValues: SpeedUnit.values)
    ..aE<MassUnit>(6, _omitFieldNames ? '' : 'mass',
        enumValues: MassUnit.values)
    ..aE<DistanceUnit>(7, _omitFieldNames ? '' : 'distance',
        enumValues: DistanceUnit.values)
    ..hasRequiredFields = false;

  @$core.Deprecated('See https://github.com/google/protobuf.dart/issues/998.')
  Preferences clone() => deepCopy();
  @$core.Deprecated('See https://github.com/google/protobuf.dart/issues/998.')
  Preferences copyWith(void Function(Preferences) updates) =>
      super.copyWith((message) => updates(message as Preferences))
          as Preferences;

  @$core.override
  $pb.BuilderInfo get info_ => _i;

  @$core.pragma('dart2js:noInline')
  @$core.Deprecated('Use Preferences() / Preferences.new instead')
  static Preferences create() => Preferences._();
  static $pb.GeneratedMessage $_createMessage() => Preferences._();
  @$core.override
  Preferences createEmptyInstance() => Preferences._();
  @$core.pragma('dart2js:noInline')
  static Preferences getDefault() =>
      _defaultInstance ??= $pb.GeneratedMessage.$_defaultFor<Preferences>(
          Preferences.$_createMessage);
  static Preferences? _defaultInstance;

  /// BCP 47, e.g. "en-CA".
  @$pb.TagNumber(1)
  $core.String get locale => $_getSZ(0);
  @$pb.TagNumber(1)
  set locale($core.String value) => $_setString(0, value);
  @$pb.TagNumber(1)
  $core.bool hasLocale() => $_has(0);
  @$pb.TagNumber(1)
  void clearLocale() => $_clearField(1);

  /// IANA, e.g. "America/Toronto".
  @$pb.TagNumber(2)
  $core.String get timeZone => $_getSZ(1);
  @$pb.TagNumber(2)
  set timeZone($core.String value) => $_setString(1, value);
  @$pb.TagNumber(2)
  $core.bool hasTimeZone() => $_has(1);
  @$pb.TagNumber(2)
  void clearTimeZone() => $_clearField(2);

  @$pb.TagNumber(3)
  HourCycle get hourCycle => $_getN(2);
  @$pb.TagNumber(3)
  set hourCycle(HourCycle value) => $_setField(3, value);
  @$pb.TagNumber(3)
  $core.bool hasHourCycle() => $_has(2);
  @$pb.TagNumber(3)
  void clearHourCycle() => $_clearField(3);

  @$pb.TagNumber(4)
  TemperatureUnit get temperature => $_getN(3);
  @$pb.TagNumber(4)
  set temperature(TemperatureUnit value) => $_setField(4, value);
  @$pb.TagNumber(4)
  $core.bool hasTemperature() => $_has(3);
  @$pb.TagNumber(4)
  void clearTemperature() => $_clearField(4);

  @$pb.TagNumber(5)
  SpeedUnit get speed => $_getN(4);
  @$pb.TagNumber(5)
  set speed(SpeedUnit value) => $_setField(5, value);
  @$pb.TagNumber(5)
  $core.bool hasSpeed() => $_has(4);
  @$pb.TagNumber(5)
  void clearSpeed() => $_clearField(5);

  @$pb.TagNumber(6)
  MassUnit get mass => $_getN(5);
  @$pb.TagNumber(6)
  set mass(MassUnit value) => $_setField(6, value);
  @$pb.TagNumber(6)
  $core.bool hasMass() => $_has(5);
  @$pb.TagNumber(6)
  void clearMass() => $_clearField(6);

  @$pb.TagNumber(7)
  DistanceUnit get distance => $_getN(6);
  @$pb.TagNumber(7)
  set distance(DistanceUnit value) => $_setField(7, value);
  @$pb.TagNumber(7)
  $core.bool hasDistance() => $_has(6);
  @$pb.TagNumber(7)
  void clearDistance() => $_clearField(7);
}

const $core.bool _omitFieldNames =
    $core.bool.fromEnvironment('protobuf.omit_field_names');
const $core.bool _omitMessageNames =
    $core.bool.fromEnvironment('protobuf.omit_message_names');
