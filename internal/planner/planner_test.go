package planner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/runspec"
)

func TestLoadAndBuildFixtureDeterministically(t *testing.T) {
	t.Parallel()

	fleet, err := LoadFile("testdata/fleet.yaml")
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	plan, err := Build(fleet)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("Plan.Validate() error = %v", err)
	}
	if plan.MutationCount != 2 || len(plan.Runs) != 2 || len(plan.Rejections) != 0 {
		t.Fatalf("plan counts = mutations %d, runs %d, rejections %d", plan.MutationCount, len(plan.Runs), len(plan.Rejections))
	}
	if plan.Runs[0].SourceRef.ID != "prod/accounts" || plan.Runs[0].TargetRef.ID != "demo-local/restore-a" {
		t.Fatalf("first placement = source %q target %q", plan.Runs[0].SourceRef.ID, plan.Runs[0].TargetRef.ID)
	}
	if plan.Runs[1].SourceRef.ID != "prod/ledger" || plan.Runs[1].TargetRef.ID != "demo-local/restore-b" {
		t.Fatalf("second placement = source %q target %q", plan.Runs[1].SourceRef.ID, plan.Runs[1].TargetRef.ID)
	}
	for _, run := range plan.Runs {
		spec, err := runspec.New(run.Spec)
		if err != nil {
			t.Fatalf("runspec.New(%q) error = %v", run.RunID, err)
		}
		if spec.Digest() != run.SpecDigest {
			t.Fatalf("run %q spec digest = %q, want %q", run.RunID, run.SpecDigest, spec.Digest())
		}
	}

	reordered := fleet
	reverse(reordered.Sources)
	reverse(reordered.TargetPools[0].Targets)
	reverse(reordered.DrillSets)
	second, err := Build(reordered)
	if err != nil {
		t.Fatalf("Build(reordered) error = %v", err)
	}
	if !reflect.DeepEqual(plan, second) {
		t.Fatalf("reordered fleet changed plan\nfirst:  %#v\nsecond: %#v", plan, second)
	}
}

func TestBuildDoesNotMutateFleetInput(t *testing.T) {
	t.Parallel()

	fleet := fixtureFleet(t)
	reverse(fleet.Sources)
	reverse(fleet.TargetPools[0].Targets)
	reverse(fleet.ProbeProfiles[0].Probes)
	before, err := json.Marshal(fleet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(fleet); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	after, err := json.Marshal(fleet)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("Build() mutated fleet input\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestBuildRecordsCapacityRejectionWithoutPartialIdentityDrift(t *testing.T) {
	t.Parallel()

	fleet := fixtureFleet(t)
	fleet.TargetPools[0].Targets = fleet.TargetPools[0].Targets[:1]
	plan, err := Build(fleet)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(plan.Runs) != 1 || plan.MutationCount != 1 {
		t.Fatalf("runs = %d, mutations = %d, want 1", len(plan.Runs), plan.MutationCount)
	}
	if len(plan.Rejections) != 1 {
		t.Fatalf("rejections = %d, want 1", len(plan.Rejections))
	}
	rejection := plan.Rejections[0]
	if rejection.SourceID != "prod/ledger" || rejection.Code != "no_compatible_target" {
		t.Fatalf("rejection = %#v", rejection)
	}

	again, err := Build(fleet)
	if err != nil {
		t.Fatalf("Build(again) error = %v", err)
	}
	if !reflect.DeepEqual(plan, again) {
		t.Fatal("identical fleet produced a different partial plan")
	}
}

func TestBuildUsesEveryTargetCapacitySlot(t *testing.T) {
	t.Parallel()

	fleet := fixtureFleet(t)
	fleet.TargetPools[0].Targets = fleet.TargetPools[0].Targets[:1]
	fleet.TargetPools[0].Targets[0].Capacity = 2

	plan, err := Build(fleet)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(plan.Runs) != 2 || plan.MutationCount != 2 || len(plan.Rejections) != 0 {
		t.Fatalf(
			"plan counts = mutations %d, runs %d, rejections %d; want 2, 2, 0",
			plan.MutationCount,
			len(plan.Runs),
			len(plan.Rejections),
		)
	}
	for index, run := range plan.Runs {
		if run.TargetRef.ID != "demo-local/restore-a" {
			t.Fatalf("run %d target = %q", index, run.TargetRef.ID)
		}
	}
}

func TestBuildRejectsDrillSetExpansionBeyondBound(t *testing.T) {
	t.Parallel()

	fleet := fixtureFleet(t)
	fleet.DrillSets[0].MaxRuns = 1
	_, err := Build(fleet)
	if err == nil || !strings.Contains(err.Error(), `drill set "critical-daily" expansion exceeds max_runs 1`) {
		t.Fatalf("Build() error = %v, want drill-set expansion bound", err)
	}
}

func TestBuildRejectsFleetExpansionBeyondBound(t *testing.T) {
	t.Parallel()

	fleet := twoSingleSourceDrillSets(t)
	_, err := Build(fleet)
	if err == nil || !strings.Contains(err.Error(), "fleet expansion exceeds max_runs 1") {
		t.Fatalf("Build() error = %v, want fleet expansion bound", err)
	}
}

func TestBuildCountsRejectedSourcesTowardDrillSetExpansionBound(t *testing.T) {
	t.Parallel()

	fleet := fixtureFleet(t)
	fleet.DrillSets[0].MaxRuns = 1
	fleet.RecoveryPolicies[0].Modes = []model.DrillMode{model.DrillModeManaged}

	_, err := Build(fleet)
	if err == nil || !strings.Contains(err.Error(), `drill set "critical-daily" expansion exceeds max_runs 1`) {
		t.Fatalf("Build() error = %v, want rejected-source drill-set expansion bound", err)
	}
}

func TestBuildCountsRejectedSourcesTowardFleetExpansionBound(t *testing.T) {
	t.Parallel()

	fleet := twoSingleSourceDrillSets(t)
	fleet.RecoveryPolicies[0].Modes = []model.DrillMode{model.DrillModeManaged}

	_, err := Build(fleet)
	if err == nil || !strings.Contains(err.Error(), "fleet expansion exceeds max_runs 1") {
		t.Fatalf("Build() error = %v, want rejected-source fleet expansion bound", err)
	}
}

func TestBuildRecordsPolicyModeMismatch(t *testing.T) {
	t.Parallel()

	fleet := fixtureFleet(t)
	fleet.RecoveryPolicies[0].Modes = []model.DrillMode{model.DrillModeManaged}
	plan, err := Build(fleet)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(plan.Runs) != 0 || len(plan.Rejections) != 2 {
		t.Fatalf("runs = %d, rejections = %d", len(plan.Runs), len(plan.Rejections))
	}
	for _, rejection := range plan.Rejections {
		if rejection.Code != "policy_mode_mismatch" {
			t.Fatalf("rejection = %#v", rejection)
		}
	}
}

func TestLoadRejectsUnknownFieldsAndMultipleDocuments(t *testing.T) {
	t.Parallel()

	payload, err := os.ReadFile("testdata/fleet.yaml")
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(payload, []byte("max_runs: 8"), []byte("max_runs: 8\nunknown_field: true"), 1)
	if _, err := Load(bytes.NewReader(unknown), "yaml"); err == nil || !strings.Contains(err.Error(), "field unknown_field not found") {
		t.Fatalf("Load(unknown) error = %v", err)
	}
	multiple := append(append([]byte(nil), payload...), []byte("\n---\n{}\n")...)
	if _, err := Load(bytes.NewReader(multiple), "yaml"); err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("Load(multiple) error = %v", err)
	}
}

func TestLoadRequiresExplicitSchemaVersion(t *testing.T) {
	t.Parallel()

	payload, err := os.ReadFile("testdata/fleet.yaml")
	if err != nil {
		t.Fatal(err)
	}
	payload = bytes.Replace(payload, []byte("schema_version: pgdrill.fleet/v1\n"), nil, 1)
	if _, err := Load(bytes.NewReader(payload), "yaml"); err == nil || !strings.Contains(err.Error(), `schema_version must be "pgdrill.fleet/v1"`) {
		t.Fatalf("Load() error = %v, want explicit schema version", err)
	}
}

func TestLoadAcceptsLegacyFleetAndEmitsStablePlan(t *testing.T) {
	t.Parallel()

	payload, err := os.ReadFile("testdata/fleet.yaml")
	if err != nil {
		t.Fatal(err)
	}
	payload = bytes.Replace(
		payload,
		[]byte("schema_version: pgdrill.fleet/v1\n"),
		[]byte("schema_version: pgdrill.fleet/v1alpha1\n"),
		1,
	)
	fleet, err := Load(bytes.NewReader(payload), "yaml")
	if err != nil {
		t.Fatalf("Load() legacy fleet error = %v", err)
	}
	plan, err := Build(fleet)
	if err != nil {
		t.Fatalf("Compile() legacy fleet error = %v", err)
	}
	if plan.SchemaVersion != CurrentPlanSchemaVersion {
		t.Fatalf("legacy fleet plan schema = %q", plan.SchemaVersion)
	}
}

func TestLoadRejectsOversizedInput(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte("x"), MaxFleetBytes+1)
	if _, err := Load(bytes.NewReader(payload), "yaml"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Load() error = %v, want size bound", err)
	}
}

func TestLoadRejectsDuplicateJSONMembers(t *testing.T) {
	t.Parallel()

	payload := `{"schema_version":"pgdrill.fleet/v1","schema_version":"pgdrill.fleet/v1alpha1"}`
	if _, err := Load(strings.NewReader(payload), "json"); err == nil ||
		!strings.Contains(err.Error(), `duplicate JSON object member "schema_version"`) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestPlanValidateRejectsTampering(t *testing.T) {
	t.Parallel()

	t.Run("spec", func(t *testing.T) {
		plan, err := Build(fixtureFleet(t))
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		plan.Runs[0].Spec.Cluster = "other"
		if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "spec_digest") {
			t.Fatalf("Validate() error = %v, want digest mismatch", err)
		}
	})

	t.Run("run id", func(t *testing.T) {
		plan, err := Build(fixtureFleet(t))
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		plan.Runs[0].RunID = "plan-tampered"
		plan.Digest, err = planDigest(plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "deterministic identity") {
			t.Fatalf("Validate() error = %v, want deterministic run identity mismatch", err)
		}
	})

	t.Run("target pool", func(t *testing.T) {
		plan, err := Build(fixtureFleet(t))
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		plan.Runs[0].TargetPoolRef.ID = "other-pool"
		plan.Digest, err = planDigest(plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "does not belong") {
			t.Fatalf("Validate() error = %v, want target-pool mismatch", err)
		}
	})

	t.Run("policy ref", func(t *testing.T) {
		plan, err := Build(fixtureFleet(t))
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		plan.Runs[0].PolicyRef.Revision = "sha256:tampered"
		plan.Digest, err = planDigest(plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "deterministic identity") {
			t.Fatalf("Validate() error = %v, want deterministic run identity mismatch", err)
		}
	})

	t.Run("complete expansion", func(t *testing.T) {
		plan, err := Build(fixtureFleet(t))
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		for index := len(plan.Runs); index <= plan.MaxRuns; index++ {
			plan.Rejections = append(plan.Rejections, Rejection{
				DrillSetID: "critical-daily",
				SourceID:   fmt.Sprintf("source-%d", index),
				Code:       "no_compatible_target",
				Message:    "no compatible target",
			})
		}
		plan.Digest, err = planDigest(plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "complete expansion exceeds") {
			t.Fatalf("Validate() error = %v, want complete expansion bound", err)
		}
	})
}

func TestFleetJSONRoundTripIsStrictAndSecretFree(t *testing.T) {
	t.Parallel()

	fleet := fixtureFleet(t)
	payload, err := json.Marshal(fleet)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bytes.ToLower(payload), []byte("password")) || bytes.Contains(bytes.ToLower(payload), []byte("environment")) && bytes.Contains(payload, []byte("WALG_")) {
		t.Fatalf("fleet payload unexpectedly exposes command secrets: %s", payload)
	}
	loaded, err := Load(bytes.NewReader(payload), "json")
	if err != nil {
		t.Fatalf("Load(json) error = %v", err)
	}
	if !reflect.DeepEqual(normalizeFleet(fleet), loaded) {
		t.Fatalf("JSON round trip changed fleet")
	}
}

func fixtureFleet(t *testing.T) Fleet {
	t.Helper()
	fleet, err := LoadFile("testdata/fleet.yaml")
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	return fleet
}

func twoSingleSourceDrillSets(t *testing.T) Fleet {
	t.Helper()
	fleet := fixtureFleet(t)
	fleet.MaxRuns = 1
	first := fleet.DrillSets[0]
	first.MaxRuns = 1
	first.SourceSelector = Selector{IDs: []string{fleet.Sources[0].ID}}
	second := first
	second.ID = "secondary-daily"
	second.SourceSelector = Selector{IDs: []string{fleet.Sources[1].ID}}
	fleet.DrillSets = []DrillSet{first, second}
	return fleet
}

func reverse[T any](values []T) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
