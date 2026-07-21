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
			Checks:   []model.Check{{Name: "ready", Status: model.CheckStatusPassed}},
			Evidence: []model.EvidenceRecord{testEvidence("ready")},
		},
	}
	second := &testProbe{
		probeType: model.ProbeSQL,
		report: model.CheckReport{
			Checks:   []model.Check{{Name: "select_1", Status: model.CheckStatusPassed}},
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
}

func TestRunProbesContinuesAfterOrdinaryProbeError(t *testing.T) {
	failed := &testProbe{
		probeType: model.ProbeSQL,
		report:    model.CheckReport{Evidence: []model.EvidenceRecord{testEvidence("failed")}},
		err:       errors.New("query failed"),
	}
	passed := &testProbe{
		probeType: model.ProbePGDump,
		report: model.CheckReport{Checks: []model.Check{{
			Name:   "schema",
			Status: model.CheckStatusPassed,
		}}},
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
	if len(report.Evidence) != 1 || report.Evidence[0].ID != "failed" {
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
				report:    model.CheckReport{Checks: []model.Check{{Name: "schema", Status: model.CheckStatusPassed}}},
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
			if report.Checks[1].Probe != model.ProbePGDump || len(report.Evidence) != 1 {
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
