package runspec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/r314tive/pgdrill/internal/model"
)

const digestPrefix = "sha256:"

var driverPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,62}[a-z0-9])?$`)

// Spec owns a normalized snapshot and never exposes its backing maps, slices,
// or canonical JSON buffer. It is safe to copy as a value.
type Spec struct {
	document  model.DrillSpec
	canonical []byte
	digest    string
}

// New creates a validated immutable spec.
func New(document model.DrillSpec) (Spec, error) {
	spec, err := Snapshot(document)
	if err != nil {
		return Spec{}, err
	}
	if err := spec.Validate(); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

// Snapshot captures invalid input as well as valid input. The engine uses this
// only to persist request-validation failures with the canonical attempted
// spec.
func Snapshot(document model.DrillSpec) (Spec, error) {
	document = normalize(document)
	canonical, err := json.Marshal(document)
	if err != nil {
		return Spec{}, fmt.Errorf("marshal canonical drill spec: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return Spec{
		document:  document,
		canonical: canonical,
		digest:    digestPrefix + hex.EncodeToString(sum[:]),
	}, nil
}

func Parse(data []byte) (Spec, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document model.DrillSpec
	if err := decoder.Decode(&document); err != nil {
		return Spec{}, fmt.Errorf("parse drill spec: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Spec{}, fmt.Errorf("parse drill spec: multiple JSON values")
		}
		return Spec{}, fmt.Errorf("parse drill spec trailing data: %w", err)
	}
	return New(document)
}

func (s Spec) Validate() error {
	if len(s.canonical) == 0 || s.digest == "" {
		return fmt.Errorf("drill spec is required")
	}
	if err := validateDocument(s.document); err != nil {
		return err
	}
	canonical, err := json.Marshal(s.document)
	if err != nil {
		return fmt.Errorf("marshal drill spec for validation: %w", err)
	}
	if !bytes.Equal(canonical, s.canonical) {
		return fmt.Errorf("drill spec canonical snapshot is inconsistent")
	}
	sum := sha256.Sum256(canonical)
	if want := digestPrefix + hex.EncodeToString(sum[:]); s.digest != want {
		return fmt.Errorf("drill spec digest %q does not match canonical content", s.digest)
	}
	return nil
}

func (s Spec) Document() model.DrillSpec {
	return cloneDocument(s.document)
}

func (s Spec) CanonicalJSON() []byte {
	return append([]byte(nil), s.canonical...)
}

func (s Spec) Digest() string {
	return s.digest
}

func ValidDigest(value string) bool {
	if len(value) != len(digestPrefix)+sha256.Size*2 || !strings.HasPrefix(value, digestPrefix) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, digestPrefix))
	return err == nil
}

func normalize(document model.DrillSpec) model.DrillSpec {
	document.SchemaVersion = strings.TrimSpace(document.SchemaVersion)
	if document.SchemaVersion == "" {
		document.SchemaVersion = model.CurrentDrillSpecSchemaVersion
	}
	document.Mode = model.DrillMode(strings.TrimSpace(string(document.Mode)))
	document.Cluster = strings.TrimSpace(document.Cluster)
	document.Source.Ref = normalizeRef(document.Source.Ref)
	document.Source.Provider = model.ProviderType(strings.TrimSpace(string(document.Source.Provider)))
	document.BackupSelection.Type = model.BackupSelectionType(strings.TrimSpace(string(document.BackupSelection.Type)))
	if document.BackupSelection.Type == "" {
		document.BackupSelection.Type = model.BackupSelectionLatestAvailable
	}
	document.BackupSelection.BackupID = strings.TrimSpace(document.BackupSelection.BackupID)
	document.Target.Ref = normalizeRef(document.Target.Ref)
	document.Target.Spec.Type = model.RestoreTargetType(strings.TrimSpace(string(document.Target.Spec.Type)))
	document.Target.Spec.WorkDir = strings.TrimSpace(document.Target.Spec.WorkDir)
	document.Target.Spec.Labels = cloneStringMap(document.Target.Spec.Labels)
	document.RecoveryTarget = normalizeRecoveryTarget(document.RecoveryTarget)
	document.Policy = normalizeRecoveryPolicy(document.Policy)
	document.ProbeProfile.Ref = normalizeRef(document.ProbeProfile.Ref)
	if len(document.ProbeProfile.Probes) == 0 {
		document.ProbeProfile.Probes = nil
	} else {
		probes := make([]model.ProbeDescriptor, len(document.ProbeProfile.Probes))
		for i, probe := range document.ProbeProfile.Probes {
			probe.Type = model.ProbeType(strings.TrimSpace(string(probe.Type)))
			probe.Name = strings.TrimSpace(probe.Name)
			if probe.Name == "" {
				probe.Name = model.DefaultProbeName(probe.Type)
			}
			probes[i] = probe
		}
		document.ProbeProfile.Probes = probes
	}
	return document
}

func normalizeRef(ref model.ComponentRef) model.ComponentRef {
	ref.ID = strings.TrimSpace(ref.ID)
	ref.Driver = strings.TrimSpace(ref.Driver)
	ref.Revision = strings.TrimSpace(ref.Revision)
	return ref
}

func normalizeRecoveryTarget(target model.RecoveryTarget) model.RecoveryTarget {
	target = target.Normalized()
	if err := target.Validate(); err != nil {
		return target
	}
	switch target.Type {
	case model.RecoveryTargetTimestamp:
		if value, err := target.Timestamp(); err == nil {
			target.Value = value.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
		}
	case model.RecoveryTargetLSN:
		target.Value = strings.ToUpper(target.Value)
	case model.RecoveryTargetXID:
		if value, err := strconv.ParseUint(target.Value, 10, 32); err == nil {
			target.Value = strconv.FormatUint(value, 10)
		}
	}
	if target.Timeline != "" && target.Timeline != "latest" && target.Timeline != "current" {
		if value, err := strconv.ParseUint(target.Timeline, 10, 32); err == nil {
			target.Timeline = strconv.FormatUint(value, 10)
		}
	}
	return target
}

func normalizeRecoveryPolicy(policy model.RecoveryPolicy) model.RecoveryPolicy {
	policy.MaximumRTO = normalizeDuration(policy.MaximumRTO)
	policy.MaximumRPO = normalizeDuration(policy.MaximumRPO)
	policy.MaximumBackupAge = normalizeDuration(policy.MaximumBackupAge)
	return policy
}

func normalizeDuration(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if duration, err := time.ParseDuration(value); err == nil {
		return duration.String()
	}
	return value
}

func validateDocument(document model.DrillSpec) error {
	if document.SchemaVersion != model.CurrentDrillSpecSchemaVersion {
		return fmt.Errorf("unsupported drill spec schema_version %q", document.SchemaVersion)
	}
	if !document.Mode.IsKnown() {
		return fmt.Errorf("unsupported drill mode %q", document.Mode)
	}
	if err := validateText("cluster", document.Cluster, false, 512); err != nil {
		return err
	}
	if err := validateRef("source.ref", document.Source.Ref); err != nil {
		return err
	}
	if document.Source.Provider != "" && !document.Source.Provider.IsKnown() {
		return fmt.Errorf("unsupported source provider %q", document.Source.Provider)
	}
	if document.Mode == model.DrillModeNative {
		if !document.Source.Provider.IsKnown() {
			return fmt.Errorf("native drill requires a known source provider")
		}
		if document.Source.Ref.Driver != string(document.Source.Provider) {
			return fmt.Errorf("native source driver %q does not match provider %q", document.Source.Ref.Driver, document.Source.Provider)
		}
	}
	if err := validateSelection(document.BackupSelection); err != nil {
		return err
	}
	if err := validateRef("target.ref", document.Target.Ref); err != nil {
		return err
	}
	if !document.Target.Spec.Type.IsKnown() {
		return fmt.Errorf("unsupported target type %q", document.Target.Spec.Type)
	}
	if document.Mode == model.DrillModeNative && document.Target.Ref.Driver != string(document.Target.Spec.Type) {
		return fmt.Errorf("native target driver %q does not match target type %q", document.Target.Ref.Driver, document.Target.Spec.Type)
	}
	for key, value := range document.Target.Spec.Labels {
		if err := validateText("target label key", key, true, 253); err != nil {
			return err
		}
		if err := validateText("target label value", value, false, 1024); err != nil {
			return err
		}
	}
	if err := document.RecoveryTarget.Validate(); err != nil {
		return fmt.Errorf("invalid recovery target: %w", err)
	}
	if err := document.Policy.Validate(); err != nil {
		return fmt.Errorf("invalid recovery policy: %w", err)
	}
	if err := validateRef("probe_profile.ref", document.ProbeProfile.Ref); err != nil {
		return err
	}
	if len(document.ProbeProfile.Probes) == 0 {
		return fmt.Errorf("probe profile requires at least one probe")
	}
	seenNames := make(map[string]struct{}, len(document.ProbeProfile.Probes))
	for i, probe := range document.ProbeProfile.Probes {
		if !probe.Type.IsKnown() {
			return fmt.Errorf("probe profile probe %d has unsupported type %q", i, probe.Type)
		}
		if err := validateText(fmt.Sprintf("probe profile probe %d name", i), probe.Name, true, 253); err != nil {
			return err
		}
		if _, ok := seenNames[probe.Name]; ok {
			return fmt.Errorf("probe profile contains duplicate probe name %q", probe.Name)
		}
		seenNames[probe.Name] = struct{}{}
	}
	return nil
}

func validateRef(field string, ref model.ComponentRef) error {
	if err := validateText(field+".id", ref.ID, true, 512); err != nil {
		return err
	}
	if !driverPattern.MatchString(ref.Driver) {
		return fmt.Errorf("%s.driver %q is invalid", field, ref.Driver)
	}
	return validateText(field+".revision", ref.Revision, true, 512)
}

func validateSelection(selection model.BackupSelection) error {
	if !selection.Type.IsKnown() {
		return fmt.Errorf("unsupported backup selection type %q", selection.Type)
	}
	switch selection.Type {
	case model.BackupSelectionLatestAvailable:
		if selection.BackupID != "" {
			return fmt.Errorf("latest_available backup selection does not accept backup_id")
		}
	case model.BackupSelectionByID:
		if err := validateText("backup_selection.backup_id", selection.BackupID, true, 1024); err != nil {
			return err
		}
	}
	return nil
}

func validateText(field, value string, required bool, maxBytes int) error {
	if required && value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", field, maxBytes)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s must not contain control characters", field)
	}
	return nil
}

func cloneDocument(document model.DrillSpec) model.DrillSpec {
	document.Target.Spec.Labels = cloneStringMap(document.Target.Spec.Labels)
	document.ProbeProfile.Probes = append([]model.ProbeDescriptor(nil), document.ProbeProfile.Probes...)
	if document.RecoveryTarget.Inclusive != nil {
		inclusive := *document.RecoveryTarget.Inclusive
		document.RecoveryTarget.Inclusive = &inclusive
	}
	return document
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
