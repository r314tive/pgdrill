package planner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/runspec"
)

func (f Fleet) Validate() error {
	if f.SchemaVersion != CurrentFleetSchemaVersion {
		return fmt.Errorf("schema_version must be %q", CurrentFleetSchemaVersion)
	}
	if f.MaxRuns < 1 || f.MaxRuns > HardMaxRuns {
		return fmt.Errorf("max_runs must be between 1 and %d", HardMaxRuns)
	}
	if len(f.Sources) == 0 {
		return fmt.Errorf("sources require at least one item")
	}
	if len(f.TargetPools) == 0 {
		return fmt.Errorf("target_pools require at least one item")
	}
	if len(f.ProbeProfiles) == 0 {
		return fmt.Errorf("probe_profiles require at least one item")
	}
	if len(f.RecoveryPolicies) == 0 {
		return fmt.Errorf("recovery_policies require at least one item")
	}
	if len(f.DrillSets) == 0 {
		return fmt.Errorf("drill_sets require at least one item")
	}

	sourceIDs := map[string]struct{}{}
	for index, source := range f.Sources {
		field := fmt.Sprintf("sources[%d]", index)
		if err := validateResource(field, source.ID, source.Revision, source.Labels); err != nil {
			return err
		}
		if _, duplicate := sourceIDs[source.ID]; duplicate {
			return fmt.Errorf("duplicate source id %q", source.ID)
		}
		sourceIDs[source.ID] = struct{}{}
		if !source.Mode.IsKnown() {
			return fmt.Errorf("%s has unsupported mode %q", field, source.Mode)
		}
		if err := validateText(field+".cluster", source.Cluster, true, 512); err != nil {
			return err
		}
		if err := validateDriver(field+".driver", source.Driver); err != nil {
			return err
		}
		if source.Provider != "" && !source.Provider.IsKnown() {
			return fmt.Errorf("%s has unsupported provider %q", field, source.Provider)
		}
		if source.Mode == model.DrillModeNative {
			if !source.Provider.IsKnown() {
				return fmt.Errorf("%s native source requires provider", field)
			}
			if source.Driver != string(source.Provider) {
				return fmt.Errorf("%s driver %q does not match provider %q", field, source.Driver, source.Provider)
			}
		}
		if err := validateResourceID(field+".execution_pool", source.ExecutionPool); err != nil {
			return err
		}
	}

	poolIDs := map[string]struct{}{}
	for poolIndex, pool := range f.TargetPools {
		field := fmt.Sprintf("target_pools[%d]", poolIndex)
		if err := validateResource(field, pool.ID, pool.Revision, pool.Labels); err != nil {
			return err
		}
		if _, duplicate := poolIDs[pool.ID]; duplicate {
			return fmt.Errorf("duplicate target pool id %q", pool.ID)
		}
		poolIDs[pool.ID] = struct{}{}
		if err := validateResourceID(field+".execution_pool", pool.ExecutionPool); err != nil {
			return err
		}
		if len(pool.Targets) == 0 {
			return fmt.Errorf("%s requires at least one target", field)
		}
		targetIDs := map[string]struct{}{}
		for targetIndex, target := range pool.Targets {
			targetField := fmt.Sprintf("%s.targets[%d]", field, targetIndex)
			if err := validateResource(targetField, target.ID, target.Revision, target.Labels); err != nil {
				return err
			}
			if _, duplicate := targetIDs[target.ID]; duplicate {
				return fmt.Errorf("%s contains duplicate target id %q", field, target.ID)
			}
			targetIDs[target.ID] = struct{}{}
			if err := validateDriver(targetField+".driver", target.Driver); err != nil {
				return err
			}
			if !target.Type.IsKnown() {
				return fmt.Errorf("%s has unsupported type %q", targetField, target.Type)
			}
			if err := validateText(targetField+".work_dir", target.WorkDir, false, 4096); err != nil {
				return err
			}
			if target.Capacity < 1 || target.Capacity > HardMaxRuns {
				return fmt.Errorf("%s.capacity must be between 1 and %d", targetField, HardMaxRuns)
			}
			if err := validateModes(targetField+".modes", target.Modes); err != nil {
				return err
			}
			if len(target.SourceDrivers) == 0 {
				return fmt.Errorf("%s.source_drivers requires at least one item", targetField)
			}
			if err := validateUniqueStrings(targetField+".source_drivers", target.SourceDrivers, validateDriver); err != nil {
				return err
			}
		}
	}

	profileIDs := map[string]struct{}{}
	for profileIndex, profile := range f.ProbeProfiles {
		field := fmt.Sprintf("probe_profiles[%d]", profileIndex)
		if err := validateResource(field, profile.ID, profile.Revision, profile.Labels); err != nil {
			return err
		}
		if _, duplicate := profileIDs[profile.ID]; duplicate {
			return fmt.Errorf("duplicate probe profile id %q", profile.ID)
		}
		profileIDs[profile.ID] = struct{}{}
		if len(profile.Probes) == 0 {
			return fmt.Errorf("%s requires at least one probe", field)
		}
		probeNames := map[string]struct{}{}
		for probeIndex, probe := range profile.Probes {
			probeField := fmt.Sprintf("%s.probes[%d]", field, probeIndex)
			if !probe.Type.IsKnown() {
				return fmt.Errorf("%s has unsupported type %q", probeField, probe.Type)
			}
			if err := validateResourceID(probeField+".name", probe.Name); err != nil {
				return err
			}
			if _, duplicate := probeNames[probe.Name]; duplicate {
				return fmt.Errorf("%s contains duplicate probe name %q", field, probe.Name)
			}
			probeNames[probe.Name] = struct{}{}
		}
	}

	policyIDs := map[string]struct{}{}
	for policyIndex, policy := range f.RecoveryPolicies {
		field := fmt.Sprintf("recovery_policies[%d]", policyIndex)
		if err := validateResource(field, policy.ID, policy.Revision, policy.Labels); err != nil {
			return err
		}
		if _, duplicate := policyIDs[policy.ID]; duplicate {
			return fmt.Errorf("duplicate recovery policy id %q", policy.ID)
		}
		policyIDs[policy.ID] = struct{}{}
		if err := validateModes(field+".modes", policy.Modes); err != nil {
			return err
		}
		selection := policy.modelBackupSelection()
		if !selection.Type.IsKnown() {
			return fmt.Errorf("%s has unsupported backup selection %q", field, selection.Type)
		}
		if selection.Type == model.BackupSelectionLatestAvailable && selection.BackupID != "" {
			return fmt.Errorf("%s latest_available selection does not accept backup_id", field)
		}
		if selection.Type == model.BackupSelectionByID {
			if err := validateText(field+".backup_selection.backup_id", selection.BackupID, true, 1024); err != nil {
				return err
			}
		}
		if err := policy.modelRecoveryTarget().Validate(); err != nil {
			return fmt.Errorf("%s has invalid recovery target: %w", field, err)
		}
		if err := policy.modelAssertions().Validate(); err != nil {
			return fmt.Errorf("%s has invalid assertions: %w", field, err)
		}
	}

	setIDs := map[string]struct{}{}
	for setIndex, set := range f.DrillSets {
		field := fmt.Sprintf("drill_sets[%d]", setIndex)
		if err := validateResource(field, set.ID, set.Revision, nil); err != nil {
			return err
		}
		if _, duplicate := setIDs[set.ID]; duplicate {
			return fmt.Errorf("duplicate drill set id %q", set.ID)
		}
		setIDs[set.ID] = struct{}{}
		if err := validateSelector(field+".source_selector", set.SourceSelector, false); err != nil {
			return err
		}
		if err := validateResourceID(field+".target_pool", set.TargetPool); err != nil {
			return err
		}
		if _, ok := poolIDs[set.TargetPool]; !ok {
			return fmt.Errorf("%s references unknown target pool %q", field, set.TargetPool)
		}
		if err := validateSelector(field+".target_selector", set.TargetSelector, true); err != nil {
			return err
		}
		if err := validateResourceID(field+".probe_profile", set.ProbeProfile); err != nil {
			return err
		}
		if _, ok := profileIDs[set.ProbeProfile]; !ok {
			return fmt.Errorf("%s references unknown probe profile %q", field, set.ProbeProfile)
		}
		if err := validateResourceID(field+".recovery_policy", set.RecoveryPolicy); err != nil {
			return err
		}
		if _, ok := policyIDs[set.RecoveryPolicy]; !ok {
			return fmt.Errorf("%s references unknown recovery policy %q", field, set.RecoveryPolicy)
		}
		if set.MaxRuns < 1 || set.MaxRuns > f.MaxRuns {
			return fmt.Errorf("%s.max_runs must be between 1 and fleet max_runs %d", field, f.MaxRuns)
		}
	}
	return nil
}

func Build(fleet Fleet) (Plan, error) {
	fleet = normalizeFleet(fleet)
	if err := fleet.Validate(); err != nil {
		return Plan{}, err
	}
	inputDigest, err := digestJSON(fleet)
	if err != nil {
		return Plan{}, fmt.Errorf("digest fleet input: %w", err)
	}

	pools := make(map[string]TargetPool, len(fleet.TargetPools))
	for _, pool := range fleet.TargetPools {
		pools[pool.ID] = pool
	}
	profiles := make(map[string]ProbeProfile, len(fleet.ProbeProfiles))
	for _, profile := range fleet.ProbeProfiles {
		profiles[profile.ID] = profile
	}
	policies := make(map[string]RecoveryPolicy, len(fleet.RecoveryPolicies))
	for _, policy := range fleet.RecoveryPolicies {
		policies[policy.ID] = policy
	}

	plan := Plan{
		SchemaVersion: CurrentPlanSchemaVersion,
		InputDigest:   inputDigest,
		MaxRuns:       fleet.MaxRuns,
		Runs:          []PlannedRun{},
	}
	assignments := map[string]int{}
	for _, set := range fleet.DrillSets {
		pool := pools[set.TargetPool]
		profile := profiles[set.ProbeProfile]
		policy := policies[set.RecoveryPolicy]
		selectedSources := selectSources(fleet.Sources, set.SourceSelector)
		if len(selectedSources) == 0 {
			plan.Rejections = append(plan.Rejections, Rejection{
				DrillSetID: set.ID,
				Code:       "no_sources",
				Message:    "source selector matched no sources",
			})
			continue
		}

		setRuns := 0
		for _, source := range selectedSources {
			if !containsMode(policy.Modes, source.Mode) {
				plan.Rejections = append(plan.Rejections, Rejection{
					DrillSetID: set.ID,
					SourceID:   source.ID,
					Code:       "policy_mode_mismatch",
					Message:    fmt.Sprintf("recovery policy %q does not support mode %q", policy.ID, source.Mode),
				})
				continue
			}
			target, found := placeTarget(source, pool, set.TargetSelector, assignments)
			if !found {
				plan.Rejections = append(plan.Rejections, Rejection{
					DrillSetID: set.ID,
					SourceID:   source.ID,
					Code:       "no_compatible_target",
					Message:    fmt.Sprintf("target pool %q has no compatible target with remaining capacity", pool.ID),
				})
				continue
			}
			setRuns++
			if setRuns > set.MaxRuns {
				return Plan{}, fmt.Errorf("drill set %q expansion exceeds max_runs %d", set.ID, set.MaxRuns)
			}
			if len(plan.Runs)+1 > fleet.MaxRuns {
				return Plan{}, fmt.Errorf("fleet expansion exceeds max_runs %d", fleet.MaxRuns)
			}

			run, err := compileRun(inputDigest, set, source, pool, target, profile, policy)
			if err != nil {
				return Plan{}, fmt.Errorf("compile drill set %q source %q: %w", set.ID, source.ID, err)
			}
			assignments[targetAssignmentKey(pool.ID, target.ID)]++
			run.Ordinal = len(plan.Runs) + 1
			plan.Runs = append(plan.Runs, run)
		}
	}
	sort.Slice(plan.Rejections, func(i, j int) bool {
		left := plan.Rejections[i]
		right := plan.Rejections[j]
		if left.DrillSetID != right.DrillSetID {
			return left.DrillSetID < right.DrillSetID
		}
		if left.SourceID != right.SourceID {
			return left.SourceID < right.SourceID
		}
		return left.Code < right.Code
	})
	plan.MutationCount = len(plan.Runs)
	plan.Digest, err = planDigest(plan)
	if err != nil {
		return Plan{}, err
	}
	if err := plan.Validate(); err != nil {
		return Plan{}, fmt.Errorf("validate compiled plan: %w", err)
	}
	return plan, nil
}

func (p Plan) Validate() error {
	if p.SchemaVersion != CurrentPlanSchemaVersion {
		return fmt.Errorf("schema_version must be %q", CurrentPlanSchemaVersion)
	}
	if !model.IsSHA256Digest(p.InputDigest) {
		return fmt.Errorf("input_digest must be a sha256 digest")
	}
	if !model.IsSHA256Digest(p.Digest) {
		return fmt.Errorf("digest must be a sha256 digest")
	}
	if p.MaxRuns < 1 || p.MaxRuns > HardMaxRuns {
		return fmt.Errorf("max_runs must be between 1 and %d", HardMaxRuns)
	}
	if len(p.Runs) > p.MaxRuns {
		return fmt.Errorf("runs exceed max_runs %d", p.MaxRuns)
	}
	if p.MutationCount != len(p.Runs) {
		return fmt.Errorf("mutation_count %d does not match run count %d", p.MutationCount, len(p.Runs))
	}
	runIDs := map[string]struct{}{}
	for index, run := range p.Runs {
		if run.Ordinal != index+1 {
			return fmt.Errorf("run %d ordinal must be %d", index, index+1)
		}
		if err := validateText(fmt.Sprintf("run %d run_id", index), run.RunID, true, 512); err != nil {
			return err
		}
		if _, duplicate := runIDs[run.RunID]; duplicate {
			return fmt.Errorf("duplicate run_id %q", run.RunID)
		}
		runIDs[run.RunID] = struct{}{}
		spec, err := runspec.New(run.Spec)
		if err != nil {
			return fmt.Errorf("run %q has invalid spec: %w", run.RunID, err)
		}
		if spec.Digest() != run.SpecDigest {
			return fmt.Errorf("run %q spec_digest does not match spec", run.RunID)
		}
		if run.SourceRef != run.Spec.Source.Ref {
			return fmt.Errorf("run %q source_ref does not match spec", run.RunID)
		}
		if run.TargetRef != run.Spec.Target.Ref {
			return fmt.Errorf("run %q target_ref does not match spec", run.RunID)
		}
		if run.ProbeRef != run.Spec.ProbeProfile.Ref {
			return fmt.Errorf("run %q probe_ref does not match spec", run.RunID)
		}
		refs := []struct {
			field string
			ref   model.ComponentRef
		}{
			{field: "drill_set_ref", ref: run.DrillSetRef},
			{field: "source_ref", ref: run.SourceRef},
			{field: "target_pool_ref", ref: run.TargetPoolRef},
			{field: "target_ref", ref: run.TargetRef},
			{field: "policy_ref", ref: run.PolicyRef},
			{field: "probe_ref", ref: run.ProbeRef},
		}
		for _, item := range refs {
			if err := validateComponentRef(item.field, item.ref); err != nil {
				return fmt.Errorf("run %q: %w", run.RunID, err)
			}
		}
		targetPrefix := run.TargetPoolRef.ID + "/"
		if !strings.HasPrefix(run.TargetRef.ID, targetPrefix) {
			return fmt.Errorf("run %q target_ref does not belong to target_pool_ref", run.RunID)
		}
		targetID := strings.TrimPrefix(run.TargetRef.ID, targetPrefix)
		if err := validateResourceID("target_ref target id", targetID); err != nil {
			return fmt.Errorf("run %q: %w", run.RunID, err)
		}
		wantRunID := plannedRunID(p.InputDigest, run)
		if run.RunID != wantRunID {
			return fmt.Errorf("run_id %q does not match deterministic identity %q", run.RunID, wantRunID)
		}
	}
	for index, rejection := range p.Rejections {
		if err := validateResourceID(fmt.Sprintf("rejection %d drill_set_id", index), rejection.DrillSetID); err != nil {
			return err
		}
		if rejection.SourceID != "" {
			if err := validateResourceID(fmt.Sprintf("rejection %d source_id", index), rejection.SourceID); err != nil {
				return err
			}
		}
		if err := validateDriver(fmt.Sprintf("rejection %d code", index), rejection.Code); err != nil {
			return err
		}
		if err := validateText(fmt.Sprintf("rejection %d message", index), rejection.Message, true, 4096); err != nil {
			return err
		}
	}
	want, err := planDigest(p)
	if err != nil {
		return err
	}
	if p.Digest != want {
		return fmt.Errorf("digest %q does not match canonical plan %q", p.Digest, want)
	}
	return nil
}

func compileRun(
	inputDigest string,
	set DrillSet,
	source BackupSource,
	pool TargetPool,
	target RestoreTarget,
	profile ProbeProfile,
	policy RecoveryPolicy,
) (PlannedRun, error) {
	sourceRef := model.ComponentRef{ID: source.ID, Driver: source.Driver, Revision: source.Revision}
	targetRef := model.ComponentRef{ID: pool.ID + "/" + target.ID, Driver: target.Driver, Revision: target.Revision}
	probeRef := model.ComponentRef{ID: profile.ID, Driver: "probe-profile", Revision: profile.Revision}
	document := model.DrillSpec{
		SchemaVersion: model.CurrentDrillSpecSchemaVersion,
		Mode:          source.Mode,
		Cluster:       source.Cluster,
		Source: model.BackupSourceSpec{
			Ref:      sourceRef,
			Provider: source.Provider,
		},
		BackupSelection: policy.modelBackupSelection(),
		Target: model.RestoreTargetSpec{
			Ref: targetRef,
			Spec: model.TargetSpec{
				Type:    target.Type,
				WorkDir: target.WorkDir,
				Labels:  cloneLabels(target.Labels),
			},
		},
		RecoveryTarget: policy.modelRecoveryTarget(),
		Policy:         policy.modelAssertions(),
		ProbeProfile: model.ProbeProfileSpec{
			Ref:    probeRef,
			Probes: profile.modelProbes(),
		},
	}
	spec, err := runspec.New(document)
	if err != nil {
		return PlannedRun{}, err
	}
	run := PlannedRun{
		DrillSetRef:   model.ComponentRef{ID: set.ID, Driver: "drill-set", Revision: set.Revision},
		SourceRef:     sourceRef,
		TargetPoolRef: model.ComponentRef{ID: pool.ID, Driver: "target-pool", Revision: pool.Revision},
		TargetRef:     targetRef,
		PolicyRef:     model.ComponentRef{ID: policy.ID, Driver: "recovery-policy", Revision: policy.Revision},
		ProbeRef:      probeRef,
		SpecDigest:    spec.Digest(),
		Spec:          spec.Document(),
	}
	run.RunID = plannedRunID(inputDigest, run)
	return run, nil
}

func selectSources(sources []BackupSource, selector Selector) []BackupSource {
	selected := make([]BackupSource, 0, len(sources))
	for _, source := range sources {
		if selectorMatches(source.ID, source.Labels, selector) {
			selected = append(selected, source)
		}
	}
	return selected
}

func placeTarget(source BackupSource, pool TargetPool, selector Selector, assignments map[string]int) (RestoreTarget, bool) {
	var selected RestoreTarget
	found := false
	selectedCount := 0
	for _, target := range pool.Targets {
		if !selectorMatches(target.ID, target.Labels, selector) {
			continue
		}
		key := targetAssignmentKey(pool.ID, target.ID)
		count := assignments[key]
		if count >= target.Capacity || !compatible(source, pool, target) {
			continue
		}
		if !found || count < selectedCount || (count == selectedCount && target.ID < selected.ID) {
			selected = target
			selectedCount = count
			found = true
		}
	}
	return selected, found
}

func compatible(source BackupSource, pool TargetPool, target RestoreTarget) bool {
	return source.ExecutionPool == pool.ExecutionPool &&
		containsMode(target.Modes, source.Mode) &&
		containsString(target.SourceDrivers, source.Driver) &&
		(source.Mode != model.DrillModeNative || target.Driver == string(target.Type))
}

func selectorMatches(id string, labels map[string]string, selector Selector) bool {
	if len(selector.IDs) > 0 && !containsString(selector.IDs, id) {
		return false
	}
	for key, value := range selector.MatchLabels {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func validateResource(field, id, revision string, labels map[string]string) error {
	if err := validateResourceID(field+".id", id); err != nil {
		return err
	}
	if err := validateText(field+".revision", revision, true, 512); err != nil {
		return err
	}
	return validateLabels(field+".labels", labels)
}

func validateLabels(field string, labels map[string]string) error {
	for key, value := range labels {
		if err := validateResourceID(field+" key", key); err != nil {
			return err
		}
		if err := validateText(field+" value", value, false, 1024); err != nil {
			return err
		}
		if value != strings.TrimSpace(value) {
			return fmt.Errorf("%s value must not contain surrounding whitespace", field)
		}
	}
	return nil
}

func validateModes(field string, modes []model.DrillMode) error {
	if len(modes) == 0 {
		return fmt.Errorf("%s requires at least one item", field)
	}
	seen := map[model.DrillMode]struct{}{}
	for _, mode := range modes {
		if !mode.IsKnown() {
			return fmt.Errorf("%s contains unsupported mode %q", field, mode)
		}
		if _, duplicate := seen[mode]; duplicate {
			return fmt.Errorf("%s contains duplicate mode %q", field, mode)
		}
		seen[mode] = struct{}{}
	}
	return nil
}

func validateUniqueStrings(field string, values []string, validate func(string, string) error) error {
	seen := map[string]struct{}{}
	for _, value := range values {
		if err := validate(field, value); err != nil {
			return err
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%s contains duplicate value %q", field, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateSelector(field string, selector Selector, allowEmpty bool) error {
	if !allowEmpty && len(selector.IDs) == 0 && len(selector.MatchLabels) == 0 {
		return fmt.Errorf("%s requires ids or match_labels", field)
	}
	if err := validateUniqueStrings(field+".ids", selector.IDs, validateResourceID); err != nil {
		return err
	}
	return validateLabels(field+".match_labels", selector.MatchLabels)
}

func validateComponentRef(field string, ref model.ComponentRef) error {
	if err := validateResourceID(field+".id", ref.ID); err != nil {
		return err
	}
	if err := validateDriver(field+".driver", ref.Driver); err != nil {
		return err
	}
	return validateText(field+".revision", ref.Revision, true, 512)
}

func (p RecoveryPolicy) modelBackupSelection() model.BackupSelection {
	return model.BackupSelection{Type: p.BackupSelection.Type, BackupID: p.BackupSelection.BackupID}
}

func (p RecoveryPolicy) modelRecoveryTarget() model.RecoveryTarget {
	var inclusive *bool
	if p.RecoveryTarget.Inclusive != nil {
		value := *p.RecoveryTarget.Inclusive
		inclusive = &value
	}
	return model.RecoveryTarget{
		Type:      p.RecoveryTarget.Type,
		Value:     p.RecoveryTarget.Value,
		Timeline:  p.RecoveryTarget.Timeline,
		Inclusive: inclusive,
	}
}

func (p RecoveryPolicy) modelAssertions() model.RecoveryPolicy {
	return model.RecoveryPolicy{
		MaximumRTO:            p.Assertions.MaximumRTO,
		MaximumRPO:            p.Assertions.MaximumRPO,
		MaximumBackupAge:      p.Assertions.MaximumBackupAge,
		RequireRecoveryTarget: p.Assertions.RequireRecoveryTarget,
		RequireCleanup:        p.Assertions.RequireCleanup,
	}
}

func (p ProbeProfile) modelProbes() []model.ProbeDescriptor {
	probes := make([]model.ProbeDescriptor, len(p.Probes))
	for index, probe := range p.Probes {
		probes[index] = model.ProbeDescriptor{Type: probe.Type, Name: probe.Name}
	}
	return probes
}

func containsMode(values []model.DrillMode, wanted model.DrillMode) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func targetAssignmentKey(poolID, targetID string) string {
	return poolID + "\x00" + targetID
}

func plannedRunID(inputDigest string, run PlannedRun) string {
	hash := sha256.New()
	parts := []string{
		inputDigest,
		run.DrillSetRef.ID,
		run.DrillSetRef.Driver,
		run.DrillSetRef.Revision,
		run.SourceRef.ID,
		run.SourceRef.Driver,
		run.SourceRef.Revision,
		run.TargetPoolRef.ID,
		run.TargetPoolRef.Driver,
		run.TargetPoolRef.Revision,
		run.TargetRef.ID,
		run.TargetRef.Driver,
		run.TargetRef.Revision,
		run.PolicyRef.ID,
		run.PolicyRef.Driver,
		run.PolicyRef.Revision,
		run.ProbeRef.ID,
		run.ProbeRef.Driver,
		run.ProbeRef.Revision,
		run.SpecDigest,
	}
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return "plan-" + hex.EncodeToString(hash.Sum(nil))[:24]
}

func digestJSON(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func planDigest(plan Plan) (string, error) {
	plan.Digest = ""
	return digestJSON(plan)
}

func cloneLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(labels))
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}
