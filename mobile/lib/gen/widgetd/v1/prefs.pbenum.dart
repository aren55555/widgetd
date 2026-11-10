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

class HourCycle extends $pb.ProtobufEnum {
  static const HourCycle HOUR_CYCLE_UNSPECIFIED =
      HourCycle._(0, _omitEnumNames ? '' : 'HOUR_CYCLE_UNSPECIFIED');
  static const HourCycle HOUR_CYCLE_H12 =
      HourCycle._(1, _omitEnumNames ? '' : 'HOUR_CYCLE_H12');
  static const HourCycle HOUR_CYCLE_H24 =
      HourCycle._(2, _omitEnumNames ? '' : 'HOUR_CYCLE_H24');

  static const $core.List<HourCycle> values = <HourCycle>[
    HOUR_CYCLE_UNSPECIFIED,
    HOUR_CYCLE_H12,
    HOUR_CYCLE_H24,
  ];

  static final $core.List<HourCycle?> _byValue =
      $pb.ProtobufEnum.$_initByValueList(values, 2);
  static HourCycle? valueOf($core.int value) =>
      value < 0 || value >= _byValue.length ? null : _byValue[value];

  const HourCycle._(super.value, super.name);
}

class TemperatureUnit extends $pb.ProtobufEnum {
  static const TemperatureUnit TEMPERATURE_UNIT_UNSPECIFIED = TemperatureUnit._(
      0, _omitEnumNames ? '' : 'TEMPERATURE_UNIT_UNSPECIFIED');
  static const TemperatureUnit TEMPERATURE_UNIT_CELSIUS =
      TemperatureUnit._(1, _omitEnumNames ? '' : 'TEMPERATURE_UNIT_CELSIUS');
  static const TemperatureUnit TEMPERATURE_UNIT_FAHRENHEIT =
      TemperatureUnit._(2, _omitEnumNames ? '' : 'TEMPERATURE_UNIT_FAHRENHEIT');

  static const $core.List<TemperatureUnit> values = <TemperatureUnit>[
    TEMPERATURE_UNIT_UNSPECIFIED,
    TEMPERATURE_UNIT_CELSIUS,
    TEMPERATURE_UNIT_FAHRENHEIT,
  ];

  static final $core.List<TemperatureUnit?> _byValue =
      $pb.ProtobufEnum.$_initByValueList(values, 2);
  static TemperatureUnit? valueOf($core.int value) =>
      value < 0 || value >= _byValue.length ? null : _byValue[value];

  const TemperatureUnit._(super.value, super.name);
}

class SpeedUnit extends $pb.ProtobufEnum {
  static const SpeedUnit SPEED_UNIT_UNSPECIFIED =
      SpeedUnit._(0, _omitEnumNames ? '' : 'SPEED_UNIT_UNSPECIFIED');
  static const SpeedUnit SPEED_UNIT_KPH =
      SpeedUnit._(1, _omitEnumNames ? '' : 'SPEED_UNIT_KPH');
  static const SpeedUnit SPEED_UNIT_MPH =
      SpeedUnit._(2, _omitEnumNames ? '' : 'SPEED_UNIT_MPH');
  static const SpeedUnit SPEED_UNIT_MPS =
      SpeedUnit._(3, _omitEnumNames ? '' : 'SPEED_UNIT_MPS');

  static const $core.List<SpeedUnit> values = <SpeedUnit>[
    SPEED_UNIT_UNSPECIFIED,
    SPEED_UNIT_KPH,
    SPEED_UNIT_MPH,
    SPEED_UNIT_MPS,
  ];

  static final $core.List<SpeedUnit?> _byValue =
      $pb.ProtobufEnum.$_initByValueList(values, 3);
  static SpeedUnit? valueOf($core.int value) =>
      value < 0 || value >= _byValue.length ? null : _byValue[value];

  const SpeedUnit._(super.value, super.name);
}

class MassUnit extends $pb.ProtobufEnum {
  static const MassUnit MASS_UNIT_UNSPECIFIED =
      MassUnit._(0, _omitEnumNames ? '' : 'MASS_UNIT_UNSPECIFIED');
  static const MassUnit MASS_UNIT_KG =
      MassUnit._(1, _omitEnumNames ? '' : 'MASS_UNIT_KG');
  static const MassUnit MASS_UNIT_LB =
      MassUnit._(2, _omitEnumNames ? '' : 'MASS_UNIT_LB');

  static const $core.List<MassUnit> values = <MassUnit>[
    MASS_UNIT_UNSPECIFIED,
    MASS_UNIT_KG,
    MASS_UNIT_LB,
  ];

  static final $core.List<MassUnit?> _byValue =
      $pb.ProtobufEnum.$_initByValueList(values, 2);
  static MassUnit? valueOf($core.int value) =>
      value < 0 || value >= _byValue.length ? null : _byValue[value];

  const MassUnit._(super.value, super.name);
}

class DistanceUnit extends $pb.ProtobufEnum {
  static const DistanceUnit DISTANCE_UNIT_UNSPECIFIED =
      DistanceUnit._(0, _omitEnumNames ? '' : 'DISTANCE_UNIT_UNSPECIFIED');
  static const DistanceUnit DISTANCE_UNIT_KM =
      DistanceUnit._(1, _omitEnumNames ? '' : 'DISTANCE_UNIT_KM');
  static const DistanceUnit DISTANCE_UNIT_MI =
      DistanceUnit._(2, _omitEnumNames ? '' : 'DISTANCE_UNIT_MI');

  static const $core.List<DistanceUnit> values = <DistanceUnit>[
    DISTANCE_UNIT_UNSPECIFIED,
    DISTANCE_UNIT_KM,
    DISTANCE_UNIT_MI,
  ];

  static final $core.List<DistanceUnit?> _byValue =
      $pb.ProtobufEnum.$_initByValueList(values, 2);
  static DistanceUnit? valueOf($core.int value) =>
      value < 0 || value >= _byValue.length ? null : _byValue[value];

  const DistanceUnit._(super.value, super.name);
}

const $core.bool _omitEnumNames =
    $core.bool.fromEnvironment('protobuf.omit_enum_names');
