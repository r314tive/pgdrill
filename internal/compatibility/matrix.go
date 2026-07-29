package compatibility

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/r314tive/pgdrill/internal/jsonutil"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/report"
	"gopkg.in/yaml.v3"
)

const (
	CurrentSchemaVersion = "pgdrill.compatibility-matrix/v1"
	LegacySchemaVersion  = "pgdrill.compatibility-matrix/v1alpha1"
)

type Component string

const (
	ComponentProvider Component = "provider"
	ComponentTarget   Component = "target"
)

func (c Component) IsKnown() bool {
	return c == ComponentProvider || c == ComponentTarget
}

type EvidenceLevel string

const (
	EvidenceLevelFixture    EvidenceLevel = "fixture"
	EvidenceLevelControlled EvidenceLevel = "controlled"
	EvidenceLevelField      EvidenceLevel = "field"
)

func (l EvidenceLevel) IsKnown() bool {
	return l == EvidenceLevelFixture || l == EvidenceLevelControlled || l == EvidenceLevelField
}

type EvidenceKind string

const (
	EvidenceKindFixture          EvidenceKind = "fixture"
	EvidenceKindConformanceTest  EvidenceKind = "conformance_test"
	EvidenceKindDrillReport      EvidenceKind = "drill_report"
	EvidenceKindFieldNote        EvidenceKind = "field_note"
	EvidenceKindRuntimeInventory EvidenceKind = "runtime_inventory"
)

func (k EvidenceKind) IsKnown() bool {
	return k == EvidenceKindFixture ||
		k == EvidenceKindConformanceTest ||
		k == EvidenceKindDrillReport ||
		k == EvidenceKindFieldNote ||
		k == EvidenceKindRuntimeInventory
}

type Matrix struct {
	SchemaVersion string  `json:"schema_version" yaml:"schema_version"`
	UpdatedAt     string  `json:"updated_at" yaml:"updated_at"`
	Entries       []Entry `json:"entries" yaml:"entries"`
}

type Entry struct {
	ID                     string                     `json:"id" yaml:"id"`
	Component              Component                  `json:"component" yaml:"component"`
	Implementation         string                     `json:"implementation" yaml:"implementation"`
	EvidenceLevel          EvidenceLevel              `json:"evidence_level" yaml:"evidence_level"`
	Capabilities           []string                   `json:"capabilities" yaml:"capabilities"`
	RecoveryTargets        []model.RecoveryTargetType `json:"recovery_targets,omitempty" yaml:"recovery_targets,omitempty"`
	ImplementationVersions []string                   `json:"implementation_versions,omitempty" yaml:"implementation_versions,omitempty"`
	PGDrillVersions        []string                   `json:"pgdrill_versions,omitempty" yaml:"pgdrill_versions,omitempty"`
	PGDrillCommits         []string                   `json:"pgdrill_commits,omitempty" yaml:"pgdrill_commits,omitempty"`
	PostgreSQLVersions     []string                   `json:"postgresql_versions,omitempty" yaml:"postgresql_versions,omitempty"`
	Platforms              []string                   `json:"platforms,omitempty" yaml:"platforms,omitempty"`
	ObservedAt             string                     `json:"observed_at,omitempty" yaml:"observed_at,omitempty"`
	Evidence               []EvidenceRef              `json:"evidence" yaml:"evidence"`
	Limitations            []string                   `json:"limitations" yaml:"limitations"`
}

type EvidenceRef struct {
	Kind EvidenceKind `json:"kind" yaml:"kind"`
	Ref  string       `json:"ref" yaml:"ref"`
}

var identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
var capabilityPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var gitCommitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var pgdrillBuildPattern = regexp.MustCompile(`^pgdrill ([^[:space:]]+) \(([0-9a-f]{40}|[0-9a-f]{64}), [^)]+\)$`)

func Parse(data []byte) (Matrix, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var matrix Matrix
	if err := decoder.Decode(&matrix); err != nil {
		return Matrix{}, fmt.Errorf("decode compatibility matrix: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Matrix{}, fmt.Errorf("decode compatibility matrix: multiple YAML documents are not allowed")
		}
		return Matrix{}, fmt.Errorf("decode compatibility matrix trailer: %w", err)
	}
	if err := matrix.Validate(); err != nil {
		return Matrix{}, err
	}
	return matrix, nil
}

func (m Matrix) Validate() error {
	if m.SchemaVersion != CurrentSchemaVersion &&
		m.SchemaVersion != LegacySchemaVersion {
		return fmt.Errorf(
			"schema_version must be %q or %q",
			CurrentSchemaVersion,
			LegacySchemaVersion,
		)
	}
	updatedAt, err := parseDate("updated_at", m.UpdatedAt)
	if err != nil {
		return err
	}
	if len(m.Entries) == 0 {
		return fmt.Errorf("entries must not be empty")
	}

	ids := make(map[string]struct{}, len(m.Entries))
	for index, entry := range m.Entries {
		if err := entry.validate(updatedAt); err != nil {
			return fmt.Errorf("entry %d: %w", index, err)
		}
		if _, exists := ids[entry.ID]; exists {
			return fmt.Errorf("entry %d: duplicate id %q", index, entry.ID)
		}
		ids[entry.ID] = struct{}{}
		if index > 0 && m.Entries[index-1].ID >= entry.ID {
			return fmt.Errorf("entries must be sorted by id: %q appears after %q", entry.ID, m.Entries[index-1].ID)
		}
	}
	return nil
}

func (e Entry) validate(updatedAt time.Time) error {
	if !identifierPattern.MatchString(e.ID) {
		return fmt.Errorf("id %q must use lowercase letters, digits, dots, underscores, or hyphens", e.ID)
	}
	if !e.Component.IsKnown() {
		return fmt.Errorf("component %q is unsupported", e.Component)
	}
	if !identifierPattern.MatchString(e.Implementation) {
		return fmt.Errorf("implementation %q is invalid", e.Implementation)
	}
	if !e.EvidenceLevel.IsKnown() {
		return fmt.Errorf("evidence_level %q is unsupported", e.EvidenceLevel)
	}
	if err := validateCapabilities(e.Capabilities); err != nil {
		return err
	}
	if err := validateRecoveryTargets(e.RecoveryTargets); err != nil {
		return err
	}
	for name, values := range map[string][]string{
		"implementation_versions": e.ImplementationVersions,
		"pgdrill_versions":        e.PGDrillVersions,
		"pgdrill_commits":         e.PGDrillCommits,
		"postgresql_versions":     e.PostgreSQLVersions,
		"platforms":               e.Platforms,
		"limitations":             e.Limitations,
	} {
		if err := validateNonemptyUnique(name, values); err != nil {
			return err
		}
	}
	for index, commit := range e.PGDrillCommits {
		if !gitCommitPattern.MatchString(commit) {
			return fmt.Errorf("pgdrill_commits %d must be a full lowercase Git object id", index)
		}
	}
	if len(e.Limitations) == 0 {
		return fmt.Errorf("limitations must not be empty")
	}
	if len(e.Evidence) == 0 {
		return fmt.Errorf("evidence must not be empty")
	}
	refs := make(map[string]struct{}, len(e.Evidence))
	for index, evidence := range e.Evidence {
		if !evidence.Kind.IsKnown() {
			return fmt.Errorf("evidence %d kind %q is unsupported", index, evidence.Kind)
		}
		if strings.TrimSpace(evidence.Ref) == "" || evidence.Ref != strings.TrimSpace(evidence.Ref) {
			return fmt.Errorf("evidence %d ref is required without surrounding whitespace", index)
		}
		key := string(evidence.Kind) + "\x00" + evidence.Ref
		if _, exists := refs[key]; exists {
			return fmt.Errorf("duplicate evidence ref %q", evidence.Ref)
		}
		refs[key] = struct{}{}
		if evidence.Kind == EvidenceKindDrillReport && e.EvidenceLevel != EvidenceLevelField {
			return fmt.Errorf("evidence %d drill_report is allowed only for field evidence", index)
		}
		if evidence.Kind == EvidenceKindRuntimeInventory && e.EvidenceLevel != EvidenceLevelField {
			return fmt.Errorf("evidence %d runtime_inventory is allowed only for field evidence", index)
		}
	}

	switch e.EvidenceLevel {
	case EvidenceLevelFixture:
		if len(e.ImplementationVersions)+len(e.PGDrillVersions)+len(e.PGDrillCommits)+len(e.PostgreSQLVersions)+len(e.Platforms) > 0 || e.ObservedAt != "" {
			return fmt.Errorf("fixture evidence must not make version, platform, or observed_at claims")
		}
		for _, evidence := range e.Evidence {
			if evidence.Kind == EvidenceKindFieldNote {
				return fmt.Errorf("fixture evidence must not contain a field note")
			}
		}
	case EvidenceLevelControlled:
		if e.ObservedAt != "" {
			return fmt.Errorf("controlled evidence must not set observed_at; use field evidence for dated external observations")
		}
	case EvidenceLevelField:
		observedAt, err := parseDate("observed_at", e.ObservedAt)
		if err != nil {
			return err
		}
		if observedAt.After(updatedAt) {
			return fmt.Errorf("observed_at %s is later than matrix updated_at %s", e.ObservedAt, updatedAt.Format(time.DateOnly))
		}
		if len(e.ImplementationVersions) == 0 || len(e.PGDrillVersions) == 0 || len(e.PGDrillCommits) == 0 || len(e.PostgreSQLVersions) == 0 || len(e.Platforms) == 0 {
			return fmt.Errorf("field evidence requires implementation, pgdrill, PostgreSQL, platform versions, and full pgdrill commits")
		}
		if len(e.ImplementationVersions) != 1 || len(e.PGDrillVersions) != 1 || len(e.PGDrillCommits) != 1 || len(e.PostgreSQLVersions) != 1 || len(e.Platforms) != 1 || len(e.RecoveryTargets) != 1 {
			return fmt.Errorf("field evidence must describe one exact implementation, pgdrill commit, PostgreSQL, platform, and recovery-target point")
		}
		foundFieldNote := false
		drillReports := 0
		runtimeInventories := 0
		for _, evidence := range e.Evidence {
			foundFieldNote = foundFieldNote || evidence.Kind == EvidenceKindFieldNote
			if evidence.Kind == EvidenceKindDrillReport {
				drillReports++
			}
			if evidence.Kind == EvidenceKindRuntimeInventory {
				runtimeInventories++
			}
		}
		if !foundFieldNote {
			return fmt.Errorf("field evidence requires a field_note reference")
		}
		if e.Component == ComponentProvider && drillReports != 1 {
			return fmt.Errorf("provider field evidence requires exactly one drill_report reference")
		}
		if runtimeInventories > 1 {
			return fmt.Errorf("field evidence permits at most one runtime_inventory reference")
		}
		if hasCapability(e, "cross_architecture_functional") && runtimeInventories != 1 {
			return fmt.Errorf("cross-architecture field evidence requires exactly one runtime_inventory reference")
		}
		if hasCapability(e, "s3_compatible_object_storage") && runtimeInventories != 1 {
			return fmt.Errorf("S3-compatible field evidence requires exactly one runtime_inventory reference")
		}
	}
	return nil
}

func (m Matrix) ValidateReferences(root string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve compatibility reference root: %w", err)
	}
	for _, entry := range m.Entries {
		for _, evidence := range entry.Evidence {
			path, err := validateReference(root, evidence.Ref)
			if err != nil {
				return fmt.Errorf("entry %q evidence %q: %w", entry.ID, evidence.Ref, err)
			}
			if evidence.Kind == EvidenceKindDrillReport {
				if err := validateDrillReport(entry, path); err != nil {
					return fmt.Errorf("entry %q evidence %q: %w", entry.ID, evidence.Ref, err)
				}
			}
			if evidence.Kind == EvidenceKindRuntimeInventory {
				if err := validateRuntimeInventory(entry, path); err != nil {
					return fmt.Errorf("entry %q evidence %q: %w", entry.ID, evidence.Ref, err)
				}
			}
		}
	}
	return nil
}

var runtimeKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validateRuntimeInventory(entry Entry, path string) error {
	values, err := readRuntimeInventory(path)
	if err != nil {
		return err
	}
	required := func(key string) (string, error) {
		value := values[key]
		if value == "" {
			return "", fmt.Errorf("runtime inventory key %q is required", key)
		}
		return value, nil
	}
	requireEqual := func(key, expected string) error {
		actual, err := required(key)
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("runtime inventory %s %q does not match %q", key, actual, expected)
		}
		return nil
	}

	platform := entry.Platforms[0]
	operatingSystem, architecture, found := strings.Cut(platform, "/")
	if !found || operatingSystem != "linux" || architecture == "" {
		return fmt.Errorf("runtime inventory validation requires a linux/<architecture> platform, got %q", platform)
	}
	for key, expected := range map[string]string{
		"build_source":                 "release_archive",
		"build_target":                 platform,
		"commit":                       entry.PGDrillCommits[0],
		"container_image_architecture": architecture,
		"version":                      entry.PGDrillVersions[0],
	} {
		if err := requireEqual(key, expected); err != nil {
			return err
		}
	}

	archive, err := required("release_archive")
	if err != nil {
		return err
	}
	expectedArchive := fmt.Sprintf(
		"pgdrill_%s_%s_%s.tar.gz",
		strings.TrimPrefix(entry.PGDrillVersions[0], "v"),
		operatingSystem,
		architecture,
	)
	if archive != expectedArchive {
		return fmt.Errorf("runtime inventory release_archive %q does not match %q", archive, expectedArchive)
	}
	for _, key := range []string{
		"container_image_id",
		"pgdrill_sha256",
		"release_archive_sha256",
	} {
		value, err := required(key)
		if err != nil {
			return err
		}
		value = strings.TrimPrefix(value, "sha256:")
		if !sha256Pattern.MatchString(value) {
			return fmt.Errorf("runtime inventory %s is not a SHA-256 digest", key)
		}
	}
	if _, err := required("go"); err != nil {
		return err
	}
	buildDate, err := required("build_date")
	if err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, buildDate); err != nil {
		return fmt.Errorf("runtime inventory build_date is not RFC3339: %w", err)
	}

	dockerArchitecture, err := required("docker_arch")
	if err != nil {
		return err
	}
	if hasCapability(entry, "cross_architecture_functional") {
		normalized, ok := normalizeArchitecture(dockerArchitecture)
		if !ok {
			return fmt.Errorf("runtime inventory docker_arch %q is unsupported", dockerArchitecture)
		}
		if normalized == architecture {
			return fmt.Errorf("runtime inventory does not prove cross-architecture execution")
		}
	}
	if hasCapability(entry, "s3_compatible_object_storage") {
		if err := requireEqual("storage_backend", "s3-compatible"); err != nil {
			return err
		}
		for _, key := range []string{"storage_endpoint", "storage_bucket", "storage_network"} {
			if _, err := required(key); err != nil {
				return err
			}
		}
	}
	return nil
}

func readRuntimeInventory(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open runtime inventory: %w", err)
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := scanner.Text()
		if line == "" {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || !runtimeKeyPattern.MatchString(key) || value == "" {
			return nil, fmt.Errorf("runtime inventory line %d must be a nonempty key=value record", lineNumber)
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("runtime inventory line %d duplicates key %q", lineNumber, key)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read runtime inventory: %w", err)
	}
	return values, nil
}

func normalizeArchitecture(value string) (string, bool) {
	switch value {
	case "amd64", "x86_64":
		return "amd64", true
	case "arm64", "aarch64":
		return "arm64", true
	default:
		return "", false
	}
}

func validateReference(root, reference string) (string, error) {
	pathPart, anchor, _ := strings.Cut(reference, "#")
	if filepath.IsAbs(pathPart) {
		return "", fmt.Errorf("reference path must be repository-relative")
	}
	clean := filepath.Clean(pathPart)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("reference path escapes repository root")
	}
	path := filepath.Join(root, clean)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("reference path escapes repository root")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat reference: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("reference is not a regular file")
	}
	if anchor == "" {
		return path, nil
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read reference anchor: %w", err)
	}
	switch filepath.Ext(path) {
	case ".go":
		if !bytes.Contains(payload, []byte("func "+anchor+"(")) {
			return "", fmt.Errorf("Go function anchor %q was not found", anchor)
		}
	case ".md":
		if !markdownAnchorExists(payload, anchor) {
			return "", fmt.Errorf("Markdown heading anchor %q was not found", anchor)
		}
	default:
		return "", fmt.Errorf("anchors are supported only for Go and Markdown references")
	}
	return path, nil
}

func validateDrillReport(entry Entry, path string) error {
	result, err := report.ReadJSONFile(path)
	if err != nil {
		return fmt.Errorf("read drill report: %w", err)
	}
	if result.Status != model.DrillStatusPassed {
		return fmt.Errorf("drill report status must be %q, got %q", model.DrillStatusPassed, result.Status)
	}
	if entry.Component == ComponentProvider && string(result.Provider) != entry.Implementation {
		return fmt.Errorf("drill report provider %q does not match implementation %q", result.Provider, entry.Implementation)
	}
	if entry.Component == ComponentTarget {
		if err := validateTargetDrillReport(entry, result); err != nil {
			return err
		}
	}
	if result.RecoveryTarget.Type != entry.RecoveryTargets[0] {
		return fmt.Errorf("drill report recovery target %q is not claimed by the entry", result.RecoveryTarget.Type)
	}
	if result.StartedAt.UTC().Format(time.DateOnly) != entry.ObservedAt {
		return fmt.Errorf("drill report start date %s does not match observed_at %s", result.StartedAt.UTC().Format(time.DateOnly), entry.ObservedAt)
	}
	pgdrillVersion, pgdrillCommit, err := parsePGDrillBuild(result.PGDrillVersion)
	if err != nil {
		return err
	}
	if pgdrillVersion != entry.PGDrillVersions[0] {
		return fmt.Errorf("drill report pgdrill version %q does not match claimed version %q", pgdrillVersion, entry.PGDrillVersions[0])
	}
	if pgdrillCommit != entry.PGDrillCommits[0] {
		return fmt.Errorf("drill report pgdrill commit %q does not match claimed commit %q", pgdrillCommit, entry.PGDrillCommits[0])
	}
	if entry.Component == ComponentProvider && !passedToolCheckContains(result.Checks, "tool."+entry.Implementation, entry.ImplementationVersions[0]) {
		return fmt.Errorf("drill report has no passed %s version check matching the entry", entry.Implementation)
	}
	if !hasPostgreSQLVersionEvidence(entry, result.Checks, entry.PostgreSQLVersions[0]) {
		return fmt.Errorf("drill report has no passed PostgreSQL version check matching the entry")
	}
	if hasCapability(entry, "timestamp_pitr") {
		if err := validateTimestampPITREvidence(result); err != nil {
			return err
		}
	}
	if hasCapability(entry, "s3_compatible_object_storage") {
		if err := validateS3CompatibleEvidence(result); err != nil {
			return err
		}
	}
	return nil
}

func validateS3CompatibleEvidence(result model.DrillResult) error {
	for _, evidence := range result.Evidence {
		command := evidence.Command
		if command == nil || !command.ExitStatus.Success || filepath.Base(command.Path) != "wal-g" {
			continue
		}
		backupFetch := false
		for _, arg := range command.Args {
			if arg == "backup-fetch" {
				backupFetch = true
				break
			}
		}
		if !backupFetch {
			continue
		}
		if !strings.HasPrefix(command.Env["WALG_S3_PREFIX"], "s3://") {
			return fmt.Errorf("S3-compatible evidence backup-fetch has no S3 WAL-G prefix")
		}
		if strings.TrimSpace(command.Env["AWS_ENDPOINT"]) == "" {
			return fmt.Errorf("S3-compatible evidence backup-fetch has no custom endpoint")
		}
		if command.Env["AWS_S3_FORCE_PATH_STYLE"] != "true" {
			return fmt.Errorf("S3-compatible evidence backup-fetch does not require path-style addressing")
		}
		if _, exists := command.Env["WALG_FILE_PREFIX"]; exists {
			return fmt.Errorf("S3-compatible evidence backup-fetch also configures filesystem storage")
		}
		for _, key := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
			if _, exists := command.Env[key]; exists {
				return fmt.Errorf("S3-compatible evidence exposes credential field %s", key)
			}
		}
		return nil
	}
	return fmt.Errorf("S3-compatible evidence has no successful WAL-G backup-fetch command")
}

func validateTimestampPITREvidence(result model.DrillResult) error {
	if result.RecoveryTarget.Type != model.RecoveryTargetTimestamp {
		return fmt.Errorf("timestamp PITR evidence has recovery target %q", result.RecoveryTarget.Type)
	}
	if _, err := time.Parse(time.RFC3339Nano, result.RecoveryTarget.Value); err != nil {
		return fmt.Errorf("timestamp PITR evidence has invalid recovery target value: %w", err)
	}

	boundaryProven := false
	for _, check := range result.Checks {
		if check.Name == "timestamp_boundary_replayed" &&
			check.Probe == model.ProbeSQL &&
			check.Status == model.CheckStatusPassed &&
			len(check.EvidenceIDs) > 0 {
			boundaryProven = true
			break
		}
	}
	if !boundaryProven {
		return fmt.Errorf("timestamp PITR evidence has no passed SQL boundary check with evidence")
	}
	if result.PolicyEvaluation == nil || result.PolicyEvaluation.RecoveryProvenAt == nil {
		return fmt.Errorf("timestamp PITR evidence has no recovery policy proof")
	}
	verdicts := make(map[model.PolicyAssertion]model.PolicyVerdict, len(result.PolicyEvaluation.Verdicts))
	for _, verdict := range result.PolicyEvaluation.Verdicts {
		verdicts[verdict.Assertion] = verdict
	}
	for _, assertion := range model.RecoveryPolicyAssertions() {
		verdict, ok := verdicts[assertion]
		if !ok || !verdict.Required || verdict.Status != model.PolicyVerdictPassed {
			return fmt.Errorf("timestamp PITR evidence has no passed required %s policy verdict", assertion)
		}
	}
	return nil
}

func hasPostgreSQLVersionEvidence(entry Entry, checks []model.Check, claim string) bool {
	if passedToolCheckContains(checks, "tool.postgres", claim) {
		return true
	}
	return entry.Component == ComponentTarget &&
		entry.Implementation == "cnpg" &&
		passedPostgreSQLClientVersionChecks(checks, claim) >= 2
}

func validateTargetDrillReport(entry Entry, result model.DrillResult) error {
	switch entry.Implementation {
	case "cnpg":
		if result.Target.Type != model.RestoreTargetKubernetes {
			return fmt.Errorf("drill report target %q does not match CNPG", result.Target.Type)
		}
		var ready *model.Check
		for index := range result.Checks {
			check := &result.Checks[index]
			if check.Name == "cnpg-instance-ready" && check.Status == model.CheckStatusPassed {
				ready = check
				break
			}
		}
		if ready == nil {
			return fmt.Errorf("drill report has no passed CNPG readiness check")
		}
		if len(entry.ImplementationVersions) != 1 {
			return fmt.Errorf("CNPG field entry must claim one implementation version")
		}
		if !hasCNPGVersionEvidence(result, entry.ImplementationVersions[0]) {
			return fmt.Errorf("drill report has no CNPG operator version evidence matching the entry")
		}
		if hasCapability(entry, "barman_cloud_plugin_recovery") {
			if err := validateBarmanCloudPluginEvidence(*ready, result); err != nil {
				return err
			}
		}
		return nil
	case "local":
		if result.Target.Type != model.RestoreTargetLocal {
			return fmt.Errorf("drill report target %q does not match local", result.Target.Type)
		}
		return nil
	default:
		return fmt.Errorf("target drill report implementation %q is unsupported", entry.Implementation)
	}
}

func validateBarmanCloudPluginEvidence(ready model.Check, result model.DrillResult) error {
	const pluginName = "barman-cloud.cloudnative-pg.io"
	if ready.Attributes["recovery_method"] != "plugin" {
		return fmt.Errorf("drill report does not identify plugin recovery")
	}
	if ready.Attributes["plugin"] != pluginName {
		return fmt.Errorf("drill report does not identify Barman Cloud Plugin")
	}
	for attribute, metadata := range map[string]string{
		"backup_id":           "cnpg_backup_id",
		"plugin":              "cnpg_plugin",
		"plugin_object_store": "cnpg_plugin_object_store",
		"plugin_version":      "cnpg_plugin_version",
		"recovery_method":     "cnpg_recovery_method",
	} {
		value := ready.Attributes[attribute]
		if value == "" {
			return fmt.Errorf("drill report CNPG readiness has no %s", attribute)
		}
		if result.Backup.Metadata[metadata] != value {
			return fmt.Errorf("drill report CNPG readiness %s does not match backup metadata %s", attribute, metadata)
		}
	}
	for _, artifact := range result.Artifacts {
		if artifact.MediaType == "application/yaml" &&
			artifact.RedactionState == model.ArtifactRedactionNotRequired {
			return nil
		}
	}
	return fmt.Errorf("drill report has no immutable CNPG YAML manifest artifact")
}

func hasCapability(entry Entry, capability string) bool {
	index := sort.SearchStrings(entry.Capabilities, capability)
	return index < len(entry.Capabilities) && entry.Capabilities[index] == capability
}

func hasCNPGVersionEvidence(result model.DrillResult, claim string) bool {
	for _, check := range result.Checks {
		if check.Name == "cnpg-instance-ready" &&
			check.Status == model.CheckStatusPassed &&
			check.Attributes["operator_version"] == claim {
			return true
		}
	}
	for _, evidence := range result.Evidence {
		if evidence.Source != "kubernetes" ||
			evidence.Command == nil ||
			!evidence.Command.ExitStatus.Success ||
			evidence.Command.StdoutTruncated {
			continue
		}
		var value any
		if err := jsonutil.DecodeOne([]byte(evidence.Command.Stdout), &value); err != nil {
			continue
		}
		if jsonContainsStringField(value, "cnpg.io/operatorVersion", claim) {
			return true
		}
	}
	return false
}

func jsonContainsStringField(value any, field, claim string) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == field {
				if text, ok := child.(string); ok && text == claim {
					return true
				}
			}
			if jsonContainsStringField(child, field, claim) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if jsonContainsStringField(child, field, claim) {
				return true
			}
		}
	}
	return false
}

func passedPostgreSQLClientVersionChecks(checks []model.Check, claim string) int {
	names := map[string]struct{}{
		"tool.pg_amcheck": {},
		"tool.pg_dump":    {},
		"tool.pg_isready": {},
		"tool.psql":       {},
	}
	passed := 0
	for name := range names {
		if passedToolCheckContains(checks, name, claim) {
			passed++
		}
	}
	return passed
}

func parsePGDrillBuild(value string) (string, string, error) {
	matches := pgdrillBuildPattern.FindStringSubmatch(value)
	if len(matches) != 3 {
		return "", "", fmt.Errorf("drill report pgdrill version %q is not a complete build identity", value)
	}
	return matches[1], matches[2], nil
}

func passedToolCheckContains(checks []model.Check, name, claim string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == model.CheckStatusPassed && versionTokenExists(check.Message, claim) {
			return true
		}
	}
	return false
}

func versionTokenExists(value, claim string) bool {
	tokens := strings.FieldsFunc(value, func(char rune) bool {
		return !unicode.IsLetter(char) && !unicode.IsDigit(char) && !strings.ContainsRune("._+-", char)
	})
	for _, token := range tokens {
		if token == claim || token == "v"+claim {
			return true
		}
	}
	return false
}

func markdownAnchorExists(payload []byte, want string) bool {
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "#") {
			continue
		}
		heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
		if markdownAnchor(heading) == want {
			return true
		}
	}
	return false
}

func markdownAnchor(heading string) string {
	var builder strings.Builder
	for _, value := range strings.ToLower(heading) {
		switch {
		case value >= 'a' && value <= 'z', value >= '0' && value <= '9', value == '-', value == '_':
			builder.WriteRune(value)
		case value == ' ':
			builder.WriteByte('-')
		}
	}
	return builder.String()
}

func validateCapabilities(values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("capabilities must not be empty")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !capabilityPattern.MatchString(value) {
			return fmt.Errorf("capability %q is invalid", value)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("duplicate capability %q", value)
		}
		seen[value] = struct{}{}
	}
	if !sort.StringsAreSorted(values) {
		return fmt.Errorf("capabilities must be sorted")
	}
	return nil
}

func validateRecoveryTargets(values []model.RecoveryTargetType) error {
	seen := make(map[model.RecoveryTargetType]struct{}, len(values))
	for _, value := range values {
		if !value.IsKnown() {
			return fmt.Errorf("recovery target %q is unsupported", value)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("duplicate recovery target %q", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateNonemptyUnique(name string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%s %d must not be empty or contain surrounding whitespace", name, index)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s contains duplicate value %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func parseDate(name, value string) (time.Time, error) {
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must use YYYY-MM-DD: %w", name, err)
	}
	return parsed, nil
}
