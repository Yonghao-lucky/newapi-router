package video_preprocess_setting

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
)

// ErrVersionConflict is returned when a save carries an expected_version that
// no longer matches the published one. The caller re-reads and retries;
// nothing was written.
var ErrVersionConflict = errors.New("video preprocessing settings version conflict")

// ErrValidationFailed is returned when a candidate document has blocking
// findings. The findings are carried by ValidationIssuesError.
var ErrValidationFailed = errors.New("video preprocessing settings validation failed")

// ErrUnknownField is returned when a document carries a key that is not part of
// the settings schema, which is almost always a typo in a field the
// administrator believes they are configuring.
var ErrUnknownField = errors.New("video preprocessing settings contain an unknown field")

// ErrMissingField is returned when a candidate document omits a schema field.
// The PUT contract is a complete candidate, so a missing key is a client bug
// that would otherwise be stored as an accidental zero value.
var ErrMissingField = errors.New("video preprocessing settings are missing a field")

// ValidationIssuesError carries the findings that blocked a save.
type ValidationIssuesError struct {
	Issues []ValidationIssue
}

func (e *ValidationIssuesError) Error() string {
	for _, issue := range e.Issues {
		if issue.Level == "error" {
			return fmt.Sprintf("%s: %s", issue.Field, issue.Message)
		}
	}
	return ErrValidationFailed.Error()
}

// Unwrap lets errors.Is(err, ErrValidationFailed) distinguish "the candidate
// was rejected" from other failures.
func (e *ValidationIssuesError) Unwrap() error { return ErrValidationFailed }

// PersistFunc writes the settings document. It is the only thing the store
// needs from the persistence layer, which is what keeps this package free of a
// dependency on model.
//
// The writer must store the row without re-entering this package's publish
// path: Save already holds the save lock while calling it.
type PersistFunc func(value string) error

// Snapshot is one published settings generation. Settings is a value with
// cloned slices, so a Snapshot handed to a caller never aliases the live one.
type Snapshot struct {
	Version  int64    `json:"version"`
	Settings Settings `json:"settings"`
}

// document is the persisted form: the settings plus the version they were
// published under. Keeping the version inside the single Option row means the
// counter survives a restart without a second storage mechanism.
type document struct {
	Version  int64    `json:"version"`
	Settings Settings `json:"settings"`
}

// rawDocument reads the settings member as raw JSON so unknown fields can be
// rejected before the struct decode silently drops them.
type rawDocument struct {
	Version  int64             `json:"version"`
	Settings common.RawMessage `json:"settings"`
}

// settingsKeys is the set of accepted JSON field names, derived from the struct
// tags once so decoding rejects unknown keys without a hand-maintained list.
var settingsKeys = func() map[string]struct{} {
	keys := make(map[string]struct{})
	settingsType := reflect.TypeFor[Settings]()
	for i := range settingsType.NumField() {
		tag := settingsType.Field(i).Tag.Get("json")
		if tag == "" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		keys[name] = struct{}{}
	}
	return keys
}()

var runtimeSnapshot atomic.Pointer[Snapshot]

// saveMu serializes the read-version / validate / persist / publish sequence so
// two concurrent saves cannot both pass the same expected_version check.
var saveMu sync.Mutex

func init() {
	snapshot := Snapshot{Version: 0, Settings: DefaultSettings()}
	runtimeSnapshot.Store(&snapshot)
}

// GetSnapshot returns the published generation. The settings are deep-copied,
// so a caller may hold and mutate the result without affecting this store or
// any other caller.
func GetSnapshot() Snapshot {
	snapshot := runtimeSnapshot.Load()
	if snapshot == nil {
		return Snapshot{Version: 0, Settings: DefaultSettings()}
	}
	return Snapshot{Version: snapshot.Version, Settings: snapshot.Settings.clone()}
}

// GetSettings returns just the published settings, deep-copied. This is the
// entry point the media, rewriting and admission modules should use.
func GetSettings() Settings {
	return GetSnapshot().Settings
}

// publish stores a deep copy under a version.
func publish(settings Settings, version int64) Snapshot {
	snapshot := Snapshot{Version: version, Settings: settings.clone()}
	runtimeSnapshot.Store(&snapshot)
	return Snapshot{Version: version, Settings: settings.clone()}
}

// Encode serializes a settings value for persistence.
func Encode(settings Settings, version int64) (string, error) {
	encoded, err := common.Marshal(document{Version: version, Settings: settings})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// decodeSettingsObject parses one settings JSON object over the defaults and
// reports an unknown field as an error instead of dropping it.
//
// Decoding over DefaultSettings() is what makes a stored document forward
// compatible: a field added by a later build that is absent from an older
// document keeps its default rather than silently becoming the zero value.
func decodeSettingsObject(raw common.RawMessage) (Settings, error) {
	if _, err := settingsFields(raw); err != nil {
		return Settings{}, err
	}

	settings := DefaultSettings()
	if err := common.Unmarshal(raw, &settings); err != nil {
		return Settings{}, fmt.Errorf("invalid video preprocessing settings: %w", err)
	}
	return settings, nil
}

// settingsFields returns the object's members, rejecting a non-object or an
// unknown key.
//
// The object check is explicit because decoding null or an array into a map
// succeeds with an empty result, which would otherwise read as "no fields to
// worry about" rather than as a malformed document.
func settingsFields(raw common.RawMessage) (map[string]common.RawMessage, error) {
	if common.GetJsonType(raw) != "object" {
		return nil, errors.New("invalid video preprocessing settings: expected a JSON object")
	}
	var fields map[string]common.RawMessage
	if err := common.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("invalid video preprocessing settings: %w", err)
	}
	for key := range fields {
		if _, ok := settingsKeys[key]; !ok {
			return nil, fmt.Errorf("%w: settings.%s", ErrUnknownField, key)
		}
	}
	return fields, nil
}

// DecodeSettings parses a candidate settings object as sent by the admin API.
// Unlike a stored document, it requires every schema field to be present: the
// PUT contract is a complete candidate, so a missing key is a client bug that
// would otherwise be accepted as an accidental zero value.
func DecodeSettings(raw common.RawMessage) (Settings, error) {
	if len(raw) == 0 {
		return Settings{}, fmt.Errorf("%w: settings", ErrMissingField)
	}
	fields, err := settingsFields(raw)
	if err != nil {
		return Settings{}, err
	}
	for field := range settingsKeys {
		if _, ok := fields[field]; !ok {
			return Settings{}, fmt.Errorf("%w: settings.%s", ErrMissingField, field)
		}
	}

	settings := DefaultSettings()
	if err := common.Unmarshal(raw, &settings); err != nil {
		return Settings{}, fmt.Errorf("invalid video preprocessing settings: %w", err)
	}
	return settings, nil
}

// Decode parses a persisted document into a Snapshot. Unknown fields are
// rejected rather than dropped, so a document written by a build with a newer
// schema fails loudly instead of silently losing the fields this build cannot
// see.
func Decode(value string) (Snapshot, error) {
	raw := common.RawMessage(value)
	if common.GetJsonType(raw) != "object" {
		return Snapshot{}, errors.New("invalid video preprocessing settings document: expected a JSON object")
	}
	var documentFields map[string]common.RawMessage
	if err := common.UnmarshalJsonStr(value, &documentFields); err != nil {
		return Snapshot{}, fmt.Errorf("invalid video preprocessing settings document: %w", err)
	}
	for key := range documentFields {
		if key != "version" && key != "settings" {
			return Snapshot{}, fmt.Errorf("%w: %s", ErrUnknownField, key)
		}
	}
	if version, ok := documentFields["version"]; !ok {
		return Snapshot{}, fmt.Errorf("%w: version", ErrMissingField)
	} else if common.GetJsonType(version) != "number" {
		return Snapshot{}, errors.New("invalid video preprocessing settings document: version must be a number")
	}

	var document rawDocument
	if err := common.UnmarshalJsonStr(value, &document); err != nil {
		return Snapshot{}, fmt.Errorf("invalid video preprocessing settings document: %w", err)
	}
	settings, err := decodeSettingsObject(document.Settings)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Version: document.Version, Settings: settings}, nil
}

// ValidateOption validates one persisted option value. Keys outside this
// module's namespace are not ours to judge, so they are accepted and left to
// their own validators. It never writes or publishes.
func ValidateOption(key string, value string) error {
	if key != SettingsOptionKey {
		return nil
	}
	snapshot, err := Decode(value)
	if err != nil {
		return err
	}
	settings := snapshot.Settings
	settings.Normalize()
	if issues := BlockingIssues(settings.Validate()); len(issues) > 0 {
		return &ValidationIssuesError{Issues: issues}
	}
	return nil
}

// ValidateCandidate normalizes and validates a candidate without publishing it
// or touching storage. Warnings are returned alongside errors so the Web form
// can surface both.
func ValidateCandidate(settings Settings) []ValidationIssue {
	settings.Normalize()
	return settings.Validate()
}

// PublishFromOption installs a settings document read from storage, during
// startup or a periodic option reload. A document this build cannot accept
// leaves the published snapshot untouched and reports why: a stored value that
// has become invalid — for example because MAX_REQUEST_BODY_MB was lowered
// since it was written — must not take down a running gateway.
func PublishFromOption(value string) error {
	snapshot, err := Decode(value)
	if err != nil {
		return err
	}
	settings := snapshot.Settings
	settings.Normalize()
	if issues := BlockingIssues(settings.Validate()); len(issues) > 0 {
		return &ValidationIssuesError{Issues: issues}
	}

	saveMu.Lock()
	defer saveMu.Unlock()
	// A reload may have read storage before a newer Save completed. Published
	// generations are immutable; only a newer document can replace one.
	if current := runtimeSnapshot.Load(); current != nil && snapshot.Version <= current.Version {
		return nil
	}
	publish(settings, snapshot.Version)
	return nil
}

// Save validates a complete candidate and, if it has no blocking findings,
// persists and publishes it as one generation.
//
// expectedVersion is the version the caller believes is current; a mismatch
// means another administrator saved in the meantime, and the save is refused so
// two open pages cannot silently overwrite each other.
//
// The read-version / validate / persist / publish sequence runs under one lock
// and persistence happens before the snapshot is swapped, so a failure at any
// point leaves both the stored document and the published snapshot exactly as
// they were. Nothing is written per field.
func Save(settings Settings, expectedVersion int64, persist PersistFunc) (Snapshot, error) {
	encoded, snapshot, err := saveExclusive(settings, expectedVersion, persist)
	if err != nil {
		return Snapshot{}, err
	}
	// The option-map cache is refreshed after the save lock is released, never
	// while holding it: the reload path takes the option-map lock and then the
	// save lock (see cacheDocument).
	cacheDocument(encoded)
	return snapshot, nil
}

// saveExclusive is Save's critical section. It returns the encoded document so
// the caller can refresh the option-map cache once the lock is free.
func saveExclusive(settings Settings, expectedVersion int64, persist PersistFunc) (string, Snapshot, error) {
	saveMu.Lock()
	defer saveMu.Unlock()

	current := runtimeSnapshot.Load()
	if current == nil {
		defaults := Snapshot{Version: 0, Settings: DefaultSettings()}
		current = &defaults
	}
	if expectedVersion != current.Version {
		return "", Snapshot{}, fmt.Errorf("%w: expected version %d, current version is %d",
			ErrVersionConflict, expectedVersion, current.Version)
	}

	candidate := settings
	candidate.Normalize()
	if issues := BlockingIssues(candidate.Validate()); len(issues) > 0 {
		return "", Snapshot{}, &ValidationIssuesError{Issues: issues}
	}

	nextVersion := current.Version + 1
	encoded, err := Encode(candidate, nextVersion)
	if err != nil {
		return "", Snapshot{}, err
	}
	if persist == nil {
		return "", Snapshot{}, errors.New("video preprocessing settings: no persistence writer configured")
	}
	if err := persist(encoded); err != nil {
		return "", Snapshot{}, err
	}
	return encoded, publish(candidate, nextVersion), nil
}

// cacheDocument keeps the shared option map in step with what was saved.
//
// Lock order is the point of this function. The option reload path
// (updateOptionMap) holds common.OptionMapRWMutex and then calls into this
// package, which takes saveMu. Taking the two locks in the opposite order here
// would deadlock the two paths against each other, so this runs only after
// saveMu has been released.
func cacheDocument(encoded string) {
	common.OptionMapRWMutex.Lock()
	if common.OptionMap != nil {
		common.OptionMap[SettingsOptionKey] = encoded
	}
	common.OptionMapRWMutex.Unlock()
}

// IsEnabledForModel reports whether the current settings route this model
// through video preprocessing. It exists so the master switch and the target
// model list cannot be checked separately — and therefore checked
// inconsistently — by the callers that gate the video path.
func IsEnabledForModel(model string) bool {
	settings := GetSettings()
	return settings.Enabled && settings.IsTargetModel(model)
}

// AllowsMimeType reports whether the current settings permit a media type.
func AllowsMimeType(mimeType string) bool {
	return GetSettings().AllowsMimeType(mimeType)
}

// AllowsCodec reports whether the current settings permit a video codec.
func AllowsCodec(codec string) bool {
	return GetSettings().AllowsCodec(codec)
}
