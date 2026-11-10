// This is a generated file - do not edit.
//
// Generated from widgetd/v1/board.proto.

// @dart = 3.3

// ignore_for_file: annotate_overrides, camel_case_types, comment_references
// ignore_for_file: constant_identifier_names
// ignore_for_file: curly_braces_in_flow_control_structures
// ignore_for_file: deprecated_member_use_from_same_package, library_prefixes
// ignore_for_file: non_constant_identifier_names, prefer_relative_imports

import 'dart:core' as $core;

import 'package:protobuf/protobuf.dart' as $pb;
import 'package:protobuf/well_known_types/google/protobuf/duration.pb.dart'
    as $0;

import 'prefs.pb.dart' as $1;

export 'package:protobuf/protobuf.dart' show GeneratedMessageGenericExtensions;

/// BoardOptions are a screen's settings, passed to the board page like
/// function arguments: `?o=` + unpadded base64url of the binary message.
/// Fields left unset are not written, so an empty message means no `o`.
/// A value that doesn't decode or breaks a rule is ignored as a whole.
class BoardOptions extends $pb.GeneratedMessage {
  factory BoardOptions({
    $core.int? cols,
    $0.Duration? staleAfter,
    $1.Preferences? prefs,
  }) {
    final result = BoardOptions._();
    if (cols != null) result.cols = cols;
    if (staleAfter != null) result.staleAfter = staleAfter;
    if (prefs != null) result.prefs = prefs;
    return result;
  }

  BoardOptions._();

  factory BoardOptions.fromBuffer($core.List<$core.int> data,
          [$pb.ExtensionRegistry registry = $pb.ExtensionRegistry.EMPTY]) =>
      BoardOptions()..mergeFromBuffer(data, registry);
  factory BoardOptions.fromJson($core.String json,
          [$pb.ExtensionRegistry registry = $pb.ExtensionRegistry.EMPTY]) =>
      BoardOptions()..mergeFromJson(json, registry);

  static final $pb.BuilderInfo _i = $pb.BuilderInfo(
      _omitMessageNames ? '' : 'BoardOptions',
      package: const $pb.PackageName(_omitMessageNames ? '' : 'widgetd.v1'),
      createEmptyInstance: BoardOptions.$_createMessage)
    ..aI(1, _omitFieldNames ? '' : 'cols', fieldType: $pb.PbFieldType.OU3)
    ..aOM<$0.Duration>(2, _omitFieldNames ? '' : 'staleAfter',
        subBuilder: $0.Duration.$_createMessage)
    ..aOM<$1.Preferences>(3, _omitFieldNames ? '' : 'prefs',
        subBuilder: $1.Preferences.$_createMessage)
    ..hasRequiredFields = false;

  @$core.Deprecated('See https://github.com/google/protobuf.dart/issues/998.')
  BoardOptions clone() => deepCopy();
  @$core.Deprecated('See https://github.com/google/protobuf.dart/issues/998.')
  BoardOptions copyWith(void Function(BoardOptions) updates) =>
      super.copyWith((message) => updates(message as BoardOptions))
          as BoardOptions;

  @$core.override
  $pb.BuilderInfo get info_ => _i;

  @$core.pragma('dart2js:noInline')
  @$core.Deprecated('Use BoardOptions() / BoardOptions.new instead')
  static BoardOptions create() => BoardOptions._();
  static $pb.GeneratedMessage $_createMessage() => BoardOptions._();
  @$core.override
  BoardOptions createEmptyInstance() => BoardOptions._();
  @$core.pragma('dart2js:noInline')
  static BoardOptions getDefault() =>
      _defaultInstance ??= $pb.GeneratedMessage.$_defaultFor<BoardOptions>(
          BoardOptions.$_createMessage);
  static BoardOptions? _defaultInstance;

  /// Maximum grid columns. Unset: the element's cols attribute, otherwise 4.
  @$pb.TagNumber(1)
  $core.int get cols => $_getIZ(0);
  @$pb.TagNumber(1)
  set cols($core.int value) => $_setUnsignedInt32(0, value);
  @$pb.TagNumber(1)
  $core.bool hasCols() => $_has(0);
  @$pb.TagNumber(1)
  void clearCols() => $_clearField(1);

  /// How long the stream may be down before the offline badge shows. Unset: 30 s.
  @$pb.TagNumber(2)
  $0.Duration get staleAfter => $_getN(1);
  @$pb.TagNumber(2)
  set staleAfter($0.Duration value) => $_setField(2, value);
  @$pb.TagNumber(2)
  $core.bool hasStaleAfter() => $_has(1);
  @$pb.TagNumber(2)
  void clearStaleAfter() => $_clearField(2);
  @$pb.TagNumber(2)
  $0.Duration ensureStaleAfter() => $_ensure(1);

  @$pb.TagNumber(3)
  $1.Preferences get prefs => $_getN(2);
  @$pb.TagNumber(3)
  set prefs($1.Preferences value) => $_setField(3, value);
  @$pb.TagNumber(3)
  $core.bool hasPrefs() => $_has(2);
  @$pb.TagNumber(3)
  void clearPrefs() => $_clearField(3);
  @$pb.TagNumber(3)
  $1.Preferences ensurePrefs() => $_ensure(2);
}

const $core.bool _omitFieldNames =
    $core.bool.fromEnvironment('protobuf.omit_field_names');
const $core.bool _omitMessageNames =
    $core.bool.fromEnvironment('protobuf.omit_message_names');
