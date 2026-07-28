package planner

import (
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/r314tive/pgdrill/internal/model"
)

const (
	CurrentFleetSchemaVersion = "pgdrill.fleet/v1"
	LegacyFleetSchemaVersion  = "pgdrill.fleet/v1alpha1"
	CurrentPlanSchemaVersion  = "pgdrill.plan/v1"
	LegacyPlanSchemaVersion   = "pgdrill.plan/v1alpha1"
	DefaultMaxRuns            = 100
	HardMaxRuns               = 10_000
	MaxFleetBytes             = 1 << 20
)

var (
	resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._:/-]{0,126}[A-Za-z0-9])?$`)
	driverPattern     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,62}[a-z0-9])?$`)
)

// Fleet is a secret-free planning inventory. It describes identities,
// compatibility and immutable revisions, but deliberately contains no command
// environment, credentials or provider configuration.
type Fleet struct {
	SchemaVersion    string           `json:"schema_version" yaml:"schema_version"`
	MaxRuns          int              `json:"max_runs,omitempty" yaml:"max_runs,omitempty"`
	Sources          []BackupSource   `json:"sources" yaml:"sources"`
	TargetPools      []TargetPool     `json:"target_pools" yaml:"target_pools"`
	ProbeProfiles    []ProbeProfile   `json:"probe_profiles" yaml:"probe_profiles"`
	RecoveryPolicies []RecoveryPolicy `json:"recovery_policies" yaml:"recovery_policies"`
	DrillSets        []DrillSet       `json:"drill_sets" yaml:"drill_sets"`
}

type BackupSource struct {
	ID            string             `json:"id" yaml:"id"`
	Revision      string             `json:"revision" yaml:"revision"`
	Labels        map[string]string  `json:"labels,omitempty" yaml:"labels,omitempty"`
	Mode          model.DrillMode    `json:"mode" yaml:"mode"`
	Cluster       string             `json:"cluster" yaml:"cluster"`
	Driver        string             `json:"driver" yaml:"driver"`
	Provider      model.ProviderType `json:"provider,omitempty" yaml:"provider,omitempty"`
	ExecutionPool string             `json:"execution_pool" yaml:"execution_pool"`
}

type TargetPool struct {
	ID            string            `json:"id" yaml:"id"`
	Revision      string            `json:"revision" yaml:"revision"`
	Labels        map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	ExecutionPool string            `json:"execution_pool" yaml:"execution_pool"`
	Targets       []RestoreTarget   `json:"targets" yaml:"targets"`
}

type RestoreTarget struct {
	ID            string                  `json:"id" yaml:"id"`
	Revision      string                  `json:"revision" yaml:"revision"`
	Labels        map[string]string       `json:"labels,omitempty" yaml:"labels,omitempty"`
	Driver        string                  `json:"driver" yaml:"driver"`
	Type          model.RestoreTargetType `json:"type" yaml:"type"`
	WorkDir       string                  `json:"work_dir,omitempty" yaml:"work_dir,omitempty"`
	Capacity      int                     `json:"capacity,omitempty" yaml:"capacity,omitempty"`
	Modes         []model.DrillMode       `json:"modes" yaml:"modes"`
	SourceDrivers []string                `json:"source_drivers" yaml:"source_drivers"`
}

type ProbeProfile struct {
	ID       string            `json:"id" yaml:"id"`
	Revision string            `json:"revision" yaml:"revision"`
	Labels   map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Probes   []Probe           `json:"probes" yaml:"probes"`
}

type Probe struct {
	Type model.ProbeType `json:"type" yaml:"type"`
	Name string          `json:"name,omitempty" yaml:"name,omitempty"`
}

type RecoveryPolicy struct {
	ID              string            `json:"id" yaml:"id"`
	Revision        string            `json:"revision" yaml:"revision"`
	Labels          map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Modes           []model.DrillMode `json:"modes" yaml:"modes"`
	BackupSelection BackupSelection   `json:"backup_selection" yaml:"backup_selection"`
	RecoveryTarget  RecoveryTarget    `json:"recovery_target" yaml:"recovery_target"`
	Assertions      PolicyAssertions  `json:"assertions,omitempty" yaml:"assertions,omitempty"`
}

type BackupSelection struct {
	Type     model.BackupSelectionType `json:"type" yaml:"type"`
	BackupID string                    `json:"backup_id,omitempty" yaml:"backup_id,omitempty"`
}

type RecoveryTarget struct {
	Type      model.RecoveryTargetType `json:"type" yaml:"type"`
	Value     string                   `json:"value,omitempty" yaml:"value,omitempty"`
	Timeline  string                   `json:"timeline,omitempty" yaml:"timeline,omitempty"`
	Inclusive *bool                    `json:"inclusive,omitempty" yaml:"inclusive,omitempty"`
}

type PolicyAssertions struct {
	MaximumRTO            string `json:"maximum_rto,omitempty" yaml:"maximum_rto,omitempty"`
	MaximumRPO            string `json:"maximum_rpo,omitempty" yaml:"maximum_rpo,omitempty"`
	MaximumBackupAge      string `json:"maximum_backup_age,omitempty" yaml:"maximum_backup_age,omitempty"`
	RequireRecoveryTarget bool   `json:"require_recovery_target,omitempty" yaml:"require_recovery_target,omitempty"`
	RequireCleanup        bool   `json:"require_cleanup,omitempty" yaml:"require_cleanup,omitempty"`
}

type DrillSet struct {
	ID             string   `json:"id" yaml:"id"`
	Revision       string   `json:"revision" yaml:"revision"`
	SourceSelector Selector `json:"source_selector" yaml:"source_selector"`
	TargetPool     string   `json:"target_pool" yaml:"target_pool"`
	TargetSelector Selector `json:"target_selector,omitempty" yaml:"target_selector,omitempty"`
	ProbeProfile   string   `json:"probe_profile" yaml:"probe_profile"`
	RecoveryPolicy string   `json:"recovery_policy" yaml:"recovery_policy"`
	MaxRuns        int      `json:"max_runs,omitempty" yaml:"max_runs,omitempty"`
}

// Selector is intentionally small for the first stable planner contract.
// IDs and match_labels are combined with AND semantics; values within ids use
// OR semantics. An empty target selector means every target in the pool.
type Selector struct {
	IDs         []string          `json:"ids,omitempty" yaml:"ids,omitempty"`
	MatchLabels map[string]string `json:"match_labels,omitempty" yaml:"match_labels,omitempty"`
}

type Plan struct {
	SchemaVersion string       `json:"schema_version"`
	InputDigest   string       `json:"input_digest"`
	Digest        string       `json:"digest"`
	MaxRuns       int          `json:"max_runs"`
	MutationCount int          `json:"mutation_count"`
	Runs          []PlannedRun `json:"runs"`
	Rejections    []Rejection  `json:"rejections,omitempty"`
}

type PlannedRun struct {
	Ordinal       int                `json:"ordinal"`
	RunID         string             `json:"run_id"`
	DrillSetRef   model.ComponentRef `json:"drill_set_ref"`
	SourceRef     model.ComponentRef `json:"source_ref"`
	TargetPoolRef model.ComponentRef `json:"target_pool_ref"`
	TargetRef     model.ComponentRef `json:"target_ref"`
	PolicyRef     model.ComponentRef `json:"policy_ref"`
	ProbeRef      model.ComponentRef `json:"probe_ref"`
	SpecDigest    string             `json:"spec_digest"`
	Spec          model.DrillSpec    `json:"spec"`
}

type Rejection struct {
	DrillSetID string `json:"drill_set_id"`
	SourceID   string `json:"source_id,omitempty"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

func normalizeFleet(fleet Fleet) Fleet {
	fleet.Sources = append([]BackupSource(nil), fleet.Sources...)
	fleet.TargetPools = append([]TargetPool(nil), fleet.TargetPools...)
	fleet.ProbeProfiles = append([]ProbeProfile(nil), fleet.ProbeProfiles...)
	fleet.RecoveryPolicies = append([]RecoveryPolicy(nil), fleet.RecoveryPolicies...)
	fleet.DrillSets = append([]DrillSet(nil), fleet.DrillSets...)
	fleet.SchemaVersion = strings.TrimSpace(fleet.SchemaVersion)
	if fleet.MaxRuns == 0 {
		fleet.MaxRuns = DefaultMaxRuns
	}
	for index := range fleet.Sources {
		source := &fleet.Sources[index]
		source.ID = strings.TrimSpace(source.ID)
		source.Revision = strings.TrimSpace(source.Revision)
		source.Labels = normalizeLabels(source.Labels)
		source.Mode = model.DrillMode(strings.TrimSpace(string(source.Mode)))
		source.Cluster = strings.TrimSpace(source.Cluster)
		source.Driver = strings.TrimSpace(source.Driver)
		source.Provider = model.ProviderType(strings.TrimSpace(string(source.Provider)))
		source.ExecutionPool = strings.TrimSpace(source.ExecutionPool)
	}
	for poolIndex := range fleet.TargetPools {
		pool := &fleet.TargetPools[poolIndex]
		pool.Targets = append([]RestoreTarget(nil), pool.Targets...)
		pool.ID = strings.TrimSpace(pool.ID)
		pool.Revision = strings.TrimSpace(pool.Revision)
		pool.Labels = normalizeLabels(pool.Labels)
		pool.ExecutionPool = strings.TrimSpace(pool.ExecutionPool)
		for targetIndex := range pool.Targets {
			target := &pool.Targets[targetIndex]
			target.ID = strings.TrimSpace(target.ID)
			target.Revision = strings.TrimSpace(target.Revision)
			target.Labels = normalizeLabels(target.Labels)
			target.Driver = strings.TrimSpace(target.Driver)
			target.Type = model.RestoreTargetType(strings.TrimSpace(string(target.Type)))
			target.WorkDir = strings.TrimSpace(target.WorkDir)
			if target.Capacity == 0 {
				target.Capacity = 1
			}
			target.Modes = normalizeModes(target.Modes)
			target.SourceDrivers = normalizeStrings(target.SourceDrivers)
		}
		sort.Slice(pool.Targets, func(i, j int) bool { return pool.Targets[i].ID < pool.Targets[j].ID })
	}
	for index := range fleet.ProbeProfiles {
		profile := &fleet.ProbeProfiles[index]
		profile.Probes = append([]Probe(nil), profile.Probes...)
		profile.ID = strings.TrimSpace(profile.ID)
		profile.Revision = strings.TrimSpace(profile.Revision)
		profile.Labels = normalizeLabels(profile.Labels)
		for probeIndex := range profile.Probes {
			probe := &profile.Probes[probeIndex]
			probe.Type = model.ProbeType(strings.TrimSpace(string(probe.Type)))
			probe.Name = strings.TrimSpace(probe.Name)
			if probe.Name == "" {
				probe.Name = model.DefaultProbeName(probe.Type)
			}
		}
	}
	for index := range fleet.RecoveryPolicies {
		policy := &fleet.RecoveryPolicies[index]
		policy.ID = strings.TrimSpace(policy.ID)
		policy.Revision = strings.TrimSpace(policy.Revision)
		policy.Labels = normalizeLabels(policy.Labels)
		policy.Modes = normalizeModes(policy.Modes)
		policy.BackupSelection.Type = model.BackupSelectionType(strings.TrimSpace(string(policy.BackupSelection.Type)))
		if policy.BackupSelection.Type == "" {
			policy.BackupSelection.Type = model.BackupSelectionLatestAvailable
		}
		policy.BackupSelection.BackupID = strings.TrimSpace(policy.BackupSelection.BackupID)
		policy.RecoveryTarget.Type = model.RecoveryTargetType(strings.TrimSpace(string(policy.RecoveryTarget.Type)))
		policy.RecoveryTarget.Value = strings.TrimSpace(policy.RecoveryTarget.Value)
		policy.RecoveryTarget.Timeline = strings.TrimSpace(policy.RecoveryTarget.Timeline)
		policy.Assertions.MaximumRTO = strings.TrimSpace(policy.Assertions.MaximumRTO)
		policy.Assertions.MaximumRPO = strings.TrimSpace(policy.Assertions.MaximumRPO)
		policy.Assertions.MaximumBackupAge = strings.TrimSpace(policy.Assertions.MaximumBackupAge)
	}
	for index := range fleet.DrillSets {
		set := &fleet.DrillSets[index]
		set.ID = strings.TrimSpace(set.ID)
		set.Revision = strings.TrimSpace(set.Revision)
		set.SourceSelector = normalizeSelector(set.SourceSelector)
		set.TargetPool = strings.TrimSpace(set.TargetPool)
		set.TargetSelector = normalizeSelector(set.TargetSelector)
		set.ProbeProfile = strings.TrimSpace(set.ProbeProfile)
		set.RecoveryPolicy = strings.TrimSpace(set.RecoveryPolicy)
		if set.MaxRuns == 0 {
			set.MaxRuns = fleet.MaxRuns
		}
	}
	sort.Slice(fleet.Sources, func(i, j int) bool { return fleet.Sources[i].ID < fleet.Sources[j].ID })
	sort.Slice(fleet.TargetPools, func(i, j int) bool { return fleet.TargetPools[i].ID < fleet.TargetPools[j].ID })
	sort.Slice(fleet.ProbeProfiles, func(i, j int) bool { return fleet.ProbeProfiles[i].ID < fleet.ProbeProfiles[j].ID })
	sort.Slice(fleet.RecoveryPolicies, func(i, j int) bool { return fleet.RecoveryPolicies[i].ID < fleet.RecoveryPolicies[j].ID })
	sort.Slice(fleet.DrillSets, func(i, j int) bool { return fleet.DrillSets[i].ID < fleet.DrillSets[j].ID })
	return fleet
}

func normalizeSelector(selector Selector) Selector {
	selector.IDs = normalizeStrings(selector.IDs)
	selector.MatchLabels = normalizeLabels(selector.MatchLabels)
	return selector
}

func normalizeModes(values []model.DrillMode) []model.DrillMode {
	if len(values) == 0 {
		return nil
	}
	normalized := make([]model.DrillMode, len(values))
	for index, value := range values {
		normalized[index] = model.DrillMode(strings.TrimSpace(string(value)))
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	return normalized
}

func normalizeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	normalized := make([]string, len(values))
	for index, value := range values {
		normalized[index] = strings.TrimSpace(value)
	}
	sort.Strings(normalized)
	return normalized
}

func normalizeLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	return maps.Clone(labels)
}

func validateResourceID(field, value string) error {
	if !resourceIDPattern.MatchString(value) {
		return fmt.Errorf("%s %q is invalid", field, value)
	}
	return nil
}

func validateDriver(field, value string) error {
	if !driverPattern.MatchString(value) {
		return fmt.Errorf("%s %q is invalid", field, value)
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
