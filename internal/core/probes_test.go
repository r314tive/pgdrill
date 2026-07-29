package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestRunProbesAggregatesChecksAndEvidence(t *testing.T) {
	first := &testProbe{
		probeType: model.ProbePGIsReady,
		report: model.CheckReport{
			Checks:   []model.Check{{Name: "ready", Status: model.CheckStatusPassed, EvidenceIDs: []string{"ready"}}},
			Evidence: []model.EvidenceRecord{testEvidence("ready")},
		},
	}
	second := &testProbe{
		probeType: model.ProbeSQL,
		report: model.CheckReport{
			Checks:   []model.Check{{Name: "select_1", Status: model.CheckStatusPassed, EvidenceIDs: []string{"select_1"}}},
			Evidence: []model.EvidenceRecord{testEvidence("select_1")},
		},
	}

	report, err := RunProbes(context.Background(), []Probe{first, second}, model.RunningPostgres{ConnString: "postgres://verify"})
	if err != nil {
		t.Fatalf("run probes: %v", err)
	}
	if len(report.Checks) != 2 || len(report.Evidence) != 2 || first.calls != 1 || second.calls != 1 {
		t.Fatalf("unexpected report=%#v calls=%d/%d", report, first.calls, second.calls)
	}
	if report.Checks[0].Probe != model.ProbePGIsReady || report.Checks[1].Probe != model.ProbeSQL {
		t.Fatalf("expected normalized probe identities, got %#v", report.Checks)
	}
	if report.Checks[0].Attributes[model.ProbeNameAttribute] != model.DefaultProbeName(model.ProbePGIsReady) ||
		report.Checks[1].Attributes[model.ProbeNameAttribute] != model.DefaultProbeName(model.ProbeSQL) {
		t.Fatalf("expected bound probe descriptors, got %#v", report.Checks)
	}
}

func TestRunProbesContinuesAfterOrdinaryProbeError(t *testing.T) {
	failed := &testProbe{
		probeType: model.ProbeSQL,
		report:    model.CheckReport{Evidence: []model.EvidenceRecord{testEvidence("failed")}},
		err:       errors.New("query failed"),
	}
	passed := &testProbe{
		probeType: model.ProbePGDump,
		report: model.CheckReport{
			Checks: []model.Check{{
				Name:        "schema",
				Status:      model.CheckStatusPassed,
				EvidenceIDs: []string{"schema"},
			}},
			Evidence: []model.EvidenceRecord{testEvidence("schema")},
		},
	}

	report, err := RunProbes(context.Background(), []Probe{failed, passed}, model.RunningPostgres{})
	if err == nil || !strings.Contains(err.Error(), "one or more probes failed") {
		t.Fatalf("expected aggregate probe failure, got %v", err)
	}
	if failed.calls != 1 || passed.calls != 1 {
		t.Fatalf("ordinary probe error must not skip later probes: calls=%d/%d", failed.calls, passed.calls)
	}
	if len(report.Checks) != 2 || report.Checks[0].Status != model.CheckStatusFailed || report.Checks[0].Message != "query failed" {
		t.Fatalf("unexpected checks %#v", report.Checks)
	}
	if len(report.Evidence) != 2 || report.Evidence[0].ID != "failed" || report.Evidence[1].ID != "schema" {
		t.Fatalf("expected failed probe evidence, got %#v", report.Evidence)
	}
}

func TestRunProbesRejectsEmptyReport(t *testing.T) {
	report, err := RunProbes(context.Background(), []Probe{&testProbe{probeType: model.ProbeSQL}}, model.RunningPostgres{})
	if err == nil || !strings.Contains(err.Error(), "one or more probes failed") {
		t.Fatalf("expected empty report failure, got %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusFailed || report.Checks[0].Message != "invalid probe report: report returned no checks" {
		t.Fatalf("expected synthesized failed check, got %#v", report.Checks)
	}
}

func TestRunProbesRejectsMalformedReportsAndContinues(t *testing.T) {
	tests := []struct {
		name  string
		check model.Check
		want  string
	}{
		{name: "unknown status", check: model.Check{Name: "bad", Status: model.CheckStatusUnknown}, want: "non-terminal status"},
		{name: "wrong probe", check: model.Check{Name: "bad", Probe: model.ProbePGDump, Status: model.CheckStatusPassed}, want: "does not match executing probe"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bad := &testProbe{
				probeType: model.ProbeSQL,
				report: model.CheckReport{
					Checks:   []model.Check{tt.check},
					Evidence: []model.EvidenceRecord{testEvidence("bad")},
				},
			}
			good := &testProbe{
				probeType: model.ProbePGDump,
				report: model.CheckReport{
					Checks: []model.Check{{
						Name:        "schema",
						Status:      model.CheckStatusPassed,
						EvidenceIDs: []string{"schema"},
					}},
					Evidence: []model.EvidenceRecord{testEvidence("schema")},
				},
			}

			report, err := RunProbes(context.Background(), []Probe{bad, good}, model.RunningPostgres{})
			if err == nil || !strings.Contains(err.Error(), "one or more probes failed") {
				t.Fatalf("expected aggregate protocol failure, got %v", err)
			}
			if bad.calls != 1 || good.calls != 1 {
				t.Fatalf("malformed report must not skip later probes: calls=%d/%d", bad.calls, good.calls)
			}
			if len(report.Checks) != 2 || report.Checks[0].Status != model.CheckStatusFailed || !strings.Contains(report.Checks[0].Message, tt.want) {
				t.Fatalf("unexpected normalized checks %#v", report.Checks)
			}
			if report.Checks[1].Probe != model.ProbePGDump || len(report.Evidence) != 2 {
				t.Fatalf("expected valid later report and malformed evidence, got %#v", report)
			}
		})
	}
}

func TestRunProbesDropsMalformedPartialChecksOnProbeError(t *testing.T) {
	probe := &testProbe{
		probeType: model.ProbeSQL,
		report: model.CheckReport{Checks: []model.Check{{
			Name:   "partial",
			Status: model.CheckStatusUnknown,
		}}},
		err: errors.New("query failed"),
	}

	report, err := RunProbes(context.Background(), []Probe{probe}, model.RunningPostgres{})
	if err == nil || !strings.Contains(err.Error(), "one or more probes failed") {
		t.Fatalf("expected aggregate probe failure, got %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusFailed {
		t.Fatalf("malformed partial check leaked into report: %#v", report.Checks)
	}
	for _, want := range []string{"query failed", "invalid partial probe report", "non-terminal status"} {
		if !strings.Contains(report.Checks[0].Message, want) {
			t.Fatalf("expected %q in synthesized check %#v", want, report.Checks[0])
		}
	}
}

func TestRunProbesStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	first := &testProbe{
		probeType: model.ProbeSQL,
		run: func(context.Context, model.RunningPostgres) (model.CheckReport, error) {
			cancel()
			return model.CheckReport{Evidence: []model.EvidenceRecord{testEvidence("canceled")}}, ctx.Err()
		},
	}
	second := &testProbe{probeType: model.ProbePGDump}

	report, err := RunProbes(ctx, []Probe{first, second}, model.RunningPostgres{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation error, got %v", err)
	}
	if first.calls != 1 || second.calls != 0 {
		t.Fatalf("cancellation must stop later probes: calls=%d/%d", first.calls, second.calls)
	}
	if len(report.Evidence) != 1 || report.Evidence[0].ID != "canceled" {
		t.Fatalf("expected partial cancellation evidence, got %#v", report.Evidence)
	}
}

func TestRunProbesRejectsNilProbe(t *testing.T) {
	_, err := RunProbes(context.Background(), []Probe{nil}, model.RunningPostgres{})
	if err == nil || !strings.Contains(err.Error(), "probe 0 is nil") {
		t.Fatalf("expected nil probe error, got %v", err)
	}
}

func TestRunProbesRejectsConflictingProbeNameAttribute(t *testing.T) {
	probe := &testProbe{
		probeType: model.ProbeSQL,
		report: model.CheckReport{Checks: []model.Check{{
			Name:       "select_1",
			Status:     model.CheckStatusPassed,
			Attributes: map[string]string{model.ProbeNameAttribute: "other-query"},
		}}},
	}

	report, err := RunProbes(context.Background(), []Probe{probe}, model.RunningPostgres{})
	if err == nil || !strings.Contains(err.Error(), "one or more probes failed") {
		t.Fatalf("expected aggregate protocol failure, got %v", err)
	}
	if len(report.Checks) != 1 ||
		report.Checks[0].Status != model.CheckStatusFailed ||
		!strings.Contains(report.Checks[0].Message, "does not match executing probe") {
		t.Fatalf("unexpected check report %#v", report)
	}
	if got := report.Checks[0].Attributes[model.ProbeNameAttribute]; got != model.DefaultProbeName(model.ProbeSQL) {
		t.Fatalf("synthetic failure probe_name = %q", got)
	}
}

func TestRunProbesRequiresPassingCheckForEachProbe(t *testing.T) {
	probe := &testProbe{
		probeType: model.ProbeSQL,
		report: model.CheckReport{Checks: []model.Check{{
			Name:   "select_1",
			Status: model.CheckStatusWarning,
		}}},
	}

	report, err := RunProbes(context.Background(), []Probe{probe}, model.RunningPostgres{})
	if err == nil || !strings.Contains(err.Error(), "one or more probes failed") {
		t.Fatalf("RunProbes() error = %v", err)
	}
	if len(report.Checks) != 1 ||
		report.Checks[0].Status != model.CheckStatusWarning ||
		report.Checks[0].Attributes[model.ProbeNameAttribute] != model.DefaultProbeName(model.ProbeSQL) {
		t.Fatalf("RunProbes() report = %#v", report)
	}
}

func TestRunProbesRejectsPassingCheckWithoutEvidence(t *testing.T) {
	probe := &testProbe{
		probeType: model.ProbeSQL,
		report: model.CheckReport{Checks: []model.Check{{
			Name:   "select_1",
			Status: model.CheckStatusPassed,
		}}},
	}

	report, err := RunProbes(context.Background(), []Probe{probe}, model.RunningPostgres{})
	if err == nil || !strings.Contains(err.Error(), "one or more probes failed") {
		t.Fatalf("RunProbes() error = %v", err)
	}
	if len(report.Checks) != 1 ||
		report.Checks[0].Status != model.CheckStatusFailed ||
		!strings.Contains(report.Checks[0].Message, "has no evidence references") {
		t.Fatalf("RunProbes() report = %#v", report)
	}
}

func TestValidateProbeEvidenceProofRejectsEvidenceSharedAcrossProbes(t *testing.T) {
	expected := []model.ProbeDescriptor{
		{Type: model.ProbeSQL, Name: "select_1"},
		{Type: model.ProbePGDump, Name: "schema_dump"},
	}
	checks := []model.Check{
		{
			Name:        "select_1",
			Probe:       model.ProbeSQL,
			Status:      model.CheckStatusPassed,
			EvidenceIDs: []string{"shared"},
			Attributes:  map[string]string{model.ProbeNameAttribute: "select_1"},
		},
		{
			Name:        "schema_dump",
			Probe:       model.ProbePGDump,
			Status:      model.CheckStatusPassed,
			EvidenceIDs: []string{"shared"},
			Attributes:  map[string]string{model.ProbeNameAttribute: "schema_dump"},
		},
	}
	evidence := []model.EvidenceRecord{testEvidence("shared")}
	evidence[0].Attributes = map[string]string{model.ProbeNameAttribute: "select_1"}

	if err := validateProbeEvidenceProof(expected, checks, evidence); err == nil ||
		!strings.Contains(err.Error(), "bound to another probe") {
		t.Fatalf("validateProbeEvidenceProof() error = %v, want shared-evidence rejection", err)
	}
}

func TestRunProbesEnforcesAggregateCheckCapacity(t *testing.T) {
	checks := make([]model.Check, model.MaxChecksPerReport)
	for index := range checks {
		checks[index] = model.Check{
			Name:        "check",
			Status:      model.CheckStatusPassed,
			EvidenceIDs: []string{"shared"},
		}
	}
	first := &testProbe{
		probeType: model.ProbeSQL,
		report: model.CheckReport{
			Checks:   checks,
			Evidence: []model.EvidenceRecord{testEvidence("shared")},
		},
	}
	second := &testProbe{probeType: model.ProbePGDump}

	report, err := RunProbes(
		context.Background(),
		[]Probe{first, second},
		model.RunningPostgres{},
	)
	if err == nil || !strings.Contains(err.Error(), "maximum is") {
		t.Fatalf("RunProbes() error = %v", err)
	}
	if len(report.Checks) != model.MaxChecksPerReport {
		t.Fatalf("retained checks = %d, want %d", len(report.Checks), model.MaxChecksPerReport)
	}
	if first.calls != 1 || second.calls != 0 {
		t.Fatalf("probe calls = %d/%d, want 1/0", first.calls, second.calls)
	}
}

type testProbe struct {
	probeType model.ProbeType
	report    model.CheckReport
	err       error
	run       func(context.Context, model.RunningPostgres) (model.CheckReport, error)
	calls     int
}

func (p *testProbe) Type() model.ProbeType {
	return p.probeType
}

func (p *testProbe) Descriptor() model.ProbeDescriptor {
	return model.ProbeDescriptor{Type: p.probeType, Name: model.DefaultProbeName(p.probeType)}
}

func (p *testProbe) Run(ctx context.Context, pg model.RunningPostgres) (model.CheckReport, error) {
	p.calls++
	if p.run != nil {
		return p.run(ctx, pg)
	}
	return p.report, p.err
}
