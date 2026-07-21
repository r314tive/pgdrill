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

	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/report"
	"gopkg.in/yaml.v3"
)

const CurrentSchemaVersion = "pgdrill.compatibility-matrix/v1alpha1"

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
	EvidenceKindFixture         EvidenceKind = "fixture"
	EvidenceKindConformanceTest EvidenceKind = "conformance_test"
	EvidenceKindDrillReport     EvidenceKind = "drill_report"
	EvidenceKindFieldNote       EvidenceKind = "field_note"
)

func (k EvidenceKind) IsKnown() bool {
	return k == EvidenceKindFixture || k == EvidenceKindConformanceTest || k == EvidenceKindDrillReport || k == EvidenceKindFieldNote
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
	if m.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("schema_version must be %q", CurrentSchemaVersion)
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
		for _, evidence := range e.Evidence {
			foundFieldNote = foundFieldNote || evidence.Kind == EvidenceKindFieldNote
			if evidence.Kind == EvidenceKindDrillReport {
				drillReports++
			}
		}
		if !foundFieldNote {
			return fmt.Errorf("field evidence requires a field_note reference")
		}
		if e.Component == ComponentProvider && drillReports != 1 {
			return fmt.Errorf("provider field evidence requires exactly one drill_report reference")
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
		}
	}
	return nil
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
	if !passedToolCheckContains(result.Checks, "tool.postgres", entry.PostgreSQLVersions[0]) {
		return fmt.Errorf("drill report has no passed PostgreSQL version check matching the entry")
	}
	return nil
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
