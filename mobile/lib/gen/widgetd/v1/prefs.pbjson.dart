// This is a generated file - do not edit.
//
// Generated from widgetd/v1/prefs.proto.

// @dart = 3.3

// ignore_for_file: annotate_overrides, camel_case_types, comment_references
// ignore_for_file: constant_identifier_names
// ignore_for_file: curly_braces_in_flow_control_structures
// ignore_for_file: deprecated_member_use_from_same_package, library_prefixes
// ignore_for_file: non_constant_identifier_names, prefer_relative_imports
// ignore_for_file: unused_import

import 'dart:convert' as $convert;
import 'dart:core' as $core;
import 'dart:typed_data' as $typed_data;

@$core.Deprecated('Use hourCycleDescriptor instead')
const HourCycle$json = {
  '1': 'HourCycle',
  '2': [
    {'1': 'HOUR_CYCLE_UNSPECIFIED', '2': 0},
    {'1': 'HOUR_CYCLE_H12', '2': 1},
    {'1': 'HOUR_CYCLE_H24', '2': 2},
  ],
};

/// Descriptor for `HourCycle`. Decode as a `google.protobuf.EnumDescriptorProto`.
final $typed_data.Uint8List hourCycleDescriptor = $convert.base64Decode(
    'CglIb3VyQ3ljbGUSGgoWSE9VUl9DWUNMRV9VTlNQRUNJRklFRBAAEhIKDkhPVVJfQ1lDTEVfSD'
    'EyEAESEgoOSE9VUl9DWUNMRV9IMjQQAg==');

@$core.Deprecated('Use temperatureUnitDescriptor instead')
const TemperatureUnit$json = {
  '1': 'TemperatureUnit',
  '2': [
    {'1': 'TEMPERATURE_UNIT_UNSPECIFIED', '2': 0},
    {'1': 'TEMPERATURE_UNIT_CELSIUS', '2': 1},
    {'1': 'TEMPERATURE_UNIT_FAHRENHEIT', '2': 2},
  ],
};

/// Descriptor for `TemperatureUnit`. Decode as a `google.protobuf.EnumDescriptorProto`.
final $typed_data.Uint8List temperatureUnitDescriptor = $convert.base64Decode(
    'Cg9UZW1wZXJhdHVyZVVuaXQSIAocVEVNUEVSQVRVUkVfVU5JVF9VTlNQRUNJRklFRBAAEhwKGF'
    'RFTVBFUkFUVVJFX1VOSVRfQ0VMU0lVUxABEh8KG1RFTVBFUkFUVVJFX1VOSVRfRkFIUkVOSEVJ'
    'VBAC');

@$core.Deprecated('Use speedUnitDescriptor instead')
const SpeedUnit$json = {
  '1': 'SpeedUnit',
  '2': [
    {'1': 'SPEED_UNIT_UNSPECIFIED', '2': 0},
    {'1': 'SPEED_UNIT_KPH', '2': 1},
    {'1': 'SPEED_UNIT_MPH', '2': 2},
    {'1': 'SPEED_UNIT_MPS', '2': 3},
  ],
};

/// Descriptor for `SpeedUnit`. Decode as a `google.protobuf.EnumDescriptorProto`.
final $typed_data.Uint8List speedUnitDescriptor = $convert.base64Decode(
    'CglTcGVlZFVuaXQSGgoWU1BFRURfVU5JVF9VTlNQRUNJRklFRBAAEhIKDlNQRUVEX1VOSVRfS1'
    'BIEAESEgoOU1BFRURfVU5JVF9NUEgQAhISCg5TUEVFRF9VTklUX01QUxAD');

@$core.Deprecated('Use massUnitDescriptor instead')
const MassUnit$json = {
  '1': 'MassUnit',
  '2': [
    {'1': 'MASS_UNIT_UNSPECIFIED', '2': 0},
    {'1': 'MASS_UNIT_KG', '2': 1},
    {'1': 'MASS_UNIT_LB', '2': 2},
  ],
};

/// Descriptor for `MassUnit`. Decode as a `google.protobuf.EnumDescriptorProto`.
final $typed_data.Uint8List massUnitDescriptor = $convert.base64Decode(
    'CghNYXNzVW5pdBIZChVNQVNTX1VOSVRfVU5TUEVDSUZJRUQQABIQCgxNQVNTX1VOSVRfS0cQAR'
    'IQCgxNQVNTX1VOSVRfTEIQAg==');

@$core.Deprecated('Use distanceUnitDescriptor instead')
const DistanceUnit$json = {
  '1': 'DistanceUnit',
  '2': [
    {'1': 'DISTANCE_UNIT_UNSPECIFIED', '2': 0},
    {'1': 'DISTANCE_UNIT_KM', '2': 1},
    {'1': 'DISTANCE_UNIT_MI', '2': 2},
  ],
};

/// Descriptor for `DistanceUnit`. Decode as a `google.protobuf.EnumDescriptorProto`.
final $typed_data.Uint8List distanceUnitDescriptor = $convert.base64Decode(
    'CgxEaXN0YW5jZVVuaXQSHQoZRElTVEFOQ0VfVU5JVF9VTlNQRUNJRklFRBAAEhQKEERJU1RBTk'
    'NFX1VOSVRfS00QARIUChBESVNUQU5DRV9VTklUX01JEAI=');

@$core.Deprecated('Use preferencesDescriptor instead')
const Preferences$json = {
  '1': 'Preferences',
  '2': [
    {'1': 'locale', '3': 1, '4': 1, '5': 9, '9': 0, '10': 'locale', '17': true},
    {
      '1': 'time_zone',
      '3': 2,
      '4': 1,
      '5': 9,
      '9': 1,
      '10': 'timeZone',
      '17': true
    },
    {
      '1': 'hour_cycle',
      '3': 3,
      '4': 1,
      '5': 14,
      '6': '.widgetd.v1.HourCycle',
      '10': 'hourCycle'
    },
    {
      '1': 'temperature',
      '3': 4,
      '4': 1,
      '5': 14,
      '6': '.widgetd.v1.TemperatureUnit',
      '10': 'temperature'
    },
    {
      '1': 'speed',
      '3': 5,
      '4': 1,
      '5': 14,
      '6': '.widgetd.v1.SpeedUnit',
      '10': 'speed'
    },
    {
      '1': 'mass',
      '3': 6,
      '4': 1,
      '5': 14,
      '6': '.widgetd.v1.MassUnit',
      '10': 'mass'
    },
    {
      '1': 'distance',
      '3': 7,
      '4': 1,
      '5': 14,
      '6': '.widgetd.v1.DistanceUnit',
      '10': 'distance'
    },
  ],
  '8': [
    {'1': '_locale'},
    {'1': '_time_zone'},
  ],
};

/// Descriptor for `Preferences`. Decode as a `google.protobuf.DescriptorProto`.
final $typed_data.Uint8List preferencesDescriptor = $convert.base64Decode(
    'CgtQcmVmZXJlbmNlcxIbCgZsb2NhbGUYASABKAlIAFIGbG9jYWxliAEBEiAKCXRpbWVfem9uZR'
    'gCIAEoCUgBUgh0aW1lWm9uZYgBARI0Cgpob3VyX2N5Y2xlGAMgASgOMhUud2lkZ2V0ZC52MS5I'
    'b3VyQ3ljbGVSCWhvdXJDeWNsZRI9Cgt0ZW1wZXJhdHVyZRgEIAEoDjIbLndpZGdldGQudjEuVG'
    'VtcGVyYXR1cmVVbml0Ugt0ZW1wZXJhdHVyZRIrCgVzcGVlZBgFIAEoDjIVLndpZGdldGQudjEu'
    'U3BlZWRVbml0UgVzcGVlZBIoCgRtYXNzGAYgASgOMhQud2lkZ2V0ZC52MS5NYXNzVW5pdFIEbW'
    'FzcxI0CghkaXN0YW5jZRgHIAEoDjIYLndpZGdldGQudjEuRGlzdGFuY2VVbml0UghkaXN0YW5j'
    'ZUIJCgdfbG9jYWxlQgwKCl90aW1lX3pvbmU=');
