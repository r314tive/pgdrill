package recoveryproof

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestNewProvidesDefaultRunner(t *testing.T) {
	verifier := New(Config{}, nil)
	if verifier == nil || verifier.runner == nil {
		t.Fatal("New() did not provide a default runner")
	}
}

func TestObservationQueryGuardsRecoveryControlFunctions(t *testing.T) {
	for _, guardedExpression := range []string{
		"WHEN in_recovery THEN pg_is_wal_replay_paused()",
		"WHEN in_recovery THEN pg_get_wal_replay_pause_state()",
	} {
		if !strings.Contains(observationQuery, guardedExpression) {
			t.Fatalf("observation query does not guard %q", guardedExpression)
		}
	}
	if !strings.Contains(observationQuery, "ELSE false") ||
		!strings.Contains(observationQuery, "ELSE 'not paused'") {
		t.Fatal("observation query does not provide completed-recovery pause state")
	}
}

func TestVerifierFailsClosedOnInvalidPreconditions(t *testing.T) {
	tests := []struct {
		name     string
		verifier *Verifier
		ctx      context.Context
		pg       model.RunningPostgres
		target   model.RecoveryTarget
		want     string
	}{
		{
			name:     "nil verifier",
			verifier: nil,
			ctx:      context.Background(),
			pg:       model.RunningPostgres{ConnString: "postgresql://verify"},
			target:   model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			want:     "verifier is required",
		},
		{
			name:     "invalid target",
			verifier: New(Config{}, &fakeRunner{}),
			ctx:      context.Background(),
			pg:       model.RunningPostgres{ConnString: "postgresql://verify"},
			target:   model.RecoveryTarget{Type: "future"},
			want:     "invalid recovery target",
		},
		{
			name:     "missing connection",
			verifier: New(Config{}, &fakeRunner{}),
			ctx:      context.Background(),
			target:   model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			want:     "conn_string is required",
		},
		{
			name:     "canceled context",
			verifier: New(Config{}, &fakeRunner{}),
			ctx:      canceledContext(),
			pg:       model.RunningPostgres{ConnString: "postgresql://verify"},
			target:   model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			want:     "observation ended",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := test.verifier.VerifyRecoveryTarget(
				test.ctx,
				test.pg,
				test.target,
			)
			if err != nil {
				t.Fatalf("VerifyRecoveryTarget() error = %v", err)
			}
			if len(report.Checks) != 1 ||
				report.Checks[0].Status != model.CheckStatusFailed ||
				!strings.Contains(report.Checks[0].Message, test.want) {
				t.Fatalf("report = %#v, want message %q", report, test.want)
			}
		})
	}
}

func TestEvaluateAcceptsCanonicalTargetStates(t *testing.T) {
	inclusive := false
	tests := []struct {
		name   string
		target model.RecoveryTarget
		state  string
	}{
		{
			name:   "latest promoted",
			target: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			state:  "recovery_complete",
		},
		{
			name:   "immediate paused",
			target: model.RecoveryTarget{Type: model.RecoveryTargetImmediate},
			state:  "paused_at_target",
		},
		{
			name: "timestamp paused",
			target: model.RecoveryTarget{
				Type:      model.RecoveryTargetTimestamp,
				Value:     "2026-07-20T06:02:03+05:00",
				Inclusive: &inclusive,
			},
			state: "paused_at_target",
		},
		{
			name:   "lsn paused",
			target: model.RecoveryTarget{Type: model.RecoveryTargetLSN, Value: "0/420000C0"},
			state:  "paused_at_target",
		},
		{
			name:   "xid paused",
			target: model.RecoveryTarget{Type: model.RecoveryTargetXID, Value: "757"},
			state:  "paused_at_target",
		},
		{
			name: "restore point paused",
			target: model.RecoveryTarget{
				Type:     model.RecoveryTargetRestorePoint,
				Value:    "before_upgrade",
				Timeline: "2",
			},
			state: "paused_at_target",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := attainedObservationFor(test.target)
			state, err := Evaluate(test.target, observation)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if state != test.state {
				t.Fatalf("Evaluate() state = %q, want %q", state, test.state)
			}
		})
	}
}

func TestEvaluateRejectsContradictoryRuntimeFacts(t *testing.T) {
	target := model.RecoveryTarget{
		Type:     model.RecoveryTargetLSN,
		Value:    "0/420000C0",
		Timeline: "2",
	}
	base := attainedObservationFor(target)
	tests := []struct {
		name   string
		mutate func(*Observation)
		want   string
	}{
		{
			name: "wrong target",
			mutate: func(value *Observation) {
				value.RecoveryTargetLSN = "0/43000000"
			},
			want: "does not match",
		},
		{
			name: "extra target",
			mutate: func(value *Observation) {
				value.RecoveryTargetName = "unexpected"
			},
			want: "unexpected configured",
		},
		{
			name: "wrong timeline",
			mutate: func(value *Observation) {
				value.RecoveryTargetTimeline = "3"
			},
			want: "timeline",
		},
		{
			name: "non-default timeline when default requested",
			mutate: func(value *Observation) {
				value.RecoveryTargetTimeline = "latest"
			},
			want: "",
		},
		{
			name: "wrong inclusive",
			mutate: func(value *Observation) {
				value.RecoveryTargetInclusive = "off"
			},
			want: "inclusive",
		},
		{
			name: "promote action",
			mutate: func(value *Observation) {
				value.RecoveryTargetAction = "promote"
			},
			want: "cannot prove",
		},
		{
			name: "targeted recovery already promoted",
			mutate: func(value *Observation) {
				value.InRecovery = false
				value.ReplayPaused = false
				value.ReplayPauseState = "not paused"
			},
			want: "left recovery",
		},
		{
			name: "pause request flag false",
			mutate: func(value *Observation) {
				value.ReplayPaused = false
			},
			want: "pause request flag is false",
		},
		{
			name: "pause requested but not actual",
			mutate: func(value *Observation) {
				value.ReplayPauseState = "pause requested"
			},
			want: "actual paused replay state",
		},
		{
			name: "not paused",
			mutate: func(value *Observation) {
				value.ReplayPaused = false
				value.ReplayPauseState = "not paused"
			},
			want: "actual paused replay state",
		},
		{
			name: "unknown pause state",
			mutate: func(value *Observation) {
				value.ReplayPauseState = "future"
			},
			want: "unsupported WAL replay pause state",
		},
		{
			name: "noncanonical pause state",
			mutate: func(value *Observation) {
				value.ReplayPauseState = "PAUSED"
			},
			want: "unsupported WAL replay pause state",
		},
		{
			name: "noncanonical action",
			mutate: func(value *Observation) {
				value.RecoveryTargetAction = "PAUSE"
			},
			want: "unsupported recovery_target_action",
		},
		{
			name: "wrong schema",
			mutate: func(value *Observation) {
				value.SchemaVersion = "future"
			},
			want: "unsupported observation",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := base
			test.mutate(&observation)
			if test.want == "" {
				defaultTimelineTarget := target
				defaultTimelineTarget.Timeline = ""
				if _, err := Evaluate(defaultTimelineTarget, observation); err != nil {
					t.Fatalf("Evaluate() error = %v", err)
				}
				observation.RecoveryTargetTimeline = "3"
				if _, err := Evaluate(defaultTimelineTarget, observation); err == nil ||
					!strings.Contains(err.Error(), "timeline") {
					t.Fatalf("Evaluate() error = %v, want timeline mismatch", err)
				}
				return
			}
			if _, err := Evaluate(target, observation); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("Evaluate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEvaluateLatestRequiresCompletedUnpausedRecovery(t *testing.T) {
	target := model.RecoveryTarget{Type: model.RecoveryTargetLatest}
	tests := []struct {
		name   string
		mutate func(*Observation)
		want   string
	}{
		{
			name: "still recovering",
			mutate: func(value *Observation) {
				value.InRecovery = true
			},
			want: "still in progress",
		},
		{
			name: "pause requested",
			mutate: func(value *Observation) {
				value.ReplayPaused = true
				value.ReplayPauseState = "pause requested"
			},
			want: "pause request",
		},
		{
			name: "paused state",
			mutate: func(value *Observation) {
				value.ReplayPauseState = "paused"
			},
			want: "pause state",
		},
		{
			name: "promote action",
			mutate: func(value *Observation) {
				value.RecoveryTargetAction = "promote"
			},
			want: "cannot prove",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := attainedObservationFor(target)
			test.mutate(&observation)
			if _, err := Evaluate(target, observation); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("Evaluate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEvaluateRejectsInvalidTargetAndAction(t *testing.T) {
	if _, err := Evaluate(
		model.RecoveryTarget{Type: "future"},
		Observation{SchemaVersion: ObservationSchema},
	); err == nil {
		t.Fatal("Evaluate() accepted invalid target")
	}

	target := model.RecoveryTarget{Type: model.RecoveryTargetImmediate}
	observation := attainedObservationFor(target)
	observation.RecoveryTargetAction = "shutdown"
	if _, err := Evaluate(target, observation); err == nil ||
		!strings.Contains(err.Error(), "requires recovery_target_action pause") {
		t.Fatalf("Evaluate() error = %v", err)
	}
}

func TestVerifierProducesSelfValidatingProof(t *testing.T) {
	target := model.RecoveryTarget{
		Type:      model.RecoveryTargetTimestamp,
		Value:     "2026-07-20T06:02:03+05:00",
		Timeline:  "latest",
		Inclusive: boolPointer(false),
	}
	observation := attainedObservationFor(target)
	payload, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	runner := &fakeRunner{result: successfulResult(payload)}
	verifier := New(Config{
		Binary:       "/usr/local/bin/psql",
		Env:          map[string]string{"PGAPPNAME": "pgdrill"},
		Timeout:      3 * time.Second,
		PollInterval: time.Millisecond,
		RedactValues: []string{"secret"},
	}, runner)

	report, err := verifier.VerifyRecoveryTarget(
		context.Background(),
		model.RunningPostgres{ConnString: "postgresql://verify"},
		target,
	)
	if err != nil {
		t.Fatalf("VerifyRecoveryTarget() error = %v", err)
	}
	if err := ValidateReport(target, report); err != nil {
		t.Fatalf("ValidateReport() error = %v", err)
	}
	if runner.invocation.Path != "/usr/local/bin/psql" ||
		runner.invocation.Timeout != 3*time.Second ||
		runner.invocation.Env["PGAPPNAME"] != "pgdrill" ||
		!reflect.DeepEqual(
			runner.invocation.Args,
			[]string{"-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-d", "postgresql://verify", "-c", observationQuery},
		) {
		t.Fatalf("invocation = %#v", runner.invocation)
	}
	if !reflect.DeepEqual(
		runner.invocation.RedactValues,
		[]string{"secret", "postgresql://verify"},
	) {
		t.Fatalf("redactions = %#v", runner.invocation.RedactValues)
	}
	if err := ValidatePersisted(
		target,
		append([]model.Check{{Name: "probe", Status: model.CheckStatusPassed}}, report.Checks...),
		report.Evidence,
	); err != nil {
		t.Fatalf("ValidatePersisted() error = %v", err)
	}
}

func TestVerifierPollsTransientRecoveryAndRetainsOnlyFinalEvidence(t *testing.T) {
	target := model.RecoveryTarget{
		Type:  model.RecoveryTargetLSN,
		Value: "0/420000C0",
	}
	transient := observationFor(target, "pause")
	transient.InRecovery = true
	transient.ReplayPaused = true
	transient.ReplayPauseState = "pause requested"
	attained := attainedObservationFor(target)
	first := resultForObservation(t, transient)
	second := resultForObservation(t, attained)
	second.Evidence.StartedAt = second.Evidence.StartedAt.Add(time.Second)
	second.Evidence.FinishedAt = second.Evidence.FinishedAt.Add(time.Second)
	runner := &fakeRunner{results: []command.Result{first, second}}

	report, err := New(Config{
		Timeout:      time.Second,
		PollInterval: time.Millisecond,
	}, runner).VerifyRecoveryTarget(
		context.Background(),
		model.RunningPostgres{ConnString: "postgresql://verify"},
		target,
	)
	if err != nil {
		t.Fatalf("VerifyRecoveryTarget() error = %v", err)
	}
	if runner.calls != 2 {
		t.Fatalf("runner calls = %d, want 2", runner.calls)
	}
	if err := ValidateReport(target, report); err != nil {
		t.Fatalf("ValidateReport() error = %v", err)
	}
	if len(report.Evidence) != 1 ||
		report.Evidence[0].Command.Stdout != second.Evidence.Stdout {
		t.Fatalf("proof did not retain only final evidence: %#v", report.Evidence)
	}
}

func TestVerifierPollsLatestUntilRecoveryCompletes(t *testing.T) {
	target := model.RecoveryTarget{Type: model.RecoveryTargetLatest}
	transient := attainedObservationFor(target)
	transient.InRecovery = true
	runner := &fakeRunner{results: []command.Result{
		resultForObservation(t, transient),
		resultForObservation(t, attainedObservationFor(target)),
	}}

	report, err := New(Config{
		Timeout:      time.Second,
		PollInterval: time.Millisecond,
	}, runner).VerifyRecoveryTarget(
		context.Background(),
		model.RunningPostgres{ConnString: "postgresql://verify"},
		target,
	)
	if err != nil {
		t.Fatalf("VerifyRecoveryTarget() error = %v", err)
	}
	if runner.calls != 2 || report.Checks[0].Status != model.CheckStatusPassed {
		t.Fatalf("latest polling report = %#v, calls = %d", report, runner.calls)
	}
}

func TestVerifierStopsImmediatelyOnFatalMismatch(t *testing.T) {
	target := model.RecoveryTarget{Type: model.RecoveryTargetLSN, Value: "0/420000C0"}
	tests := []struct {
		name   string
		mutate func(*Observation)
	}{
		{
			name: "configured target mismatch",
			mutate: func(value *Observation) {
				value.RecoveryTargetLSN = "0/43000000"
			},
		},
		{
			name: "promote action",
			mutate: func(value *Observation) {
				value.RecoveryTargetAction = "promote"
			},
		},
		{
			name: "left recovery",
			mutate: func(value *Observation) {
				value.InRecovery = false
				value.ReplayPaused = false
				value.ReplayPauseState = "not paused"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := attainedObservationFor(target)
			test.mutate(&observation)
			runner := &fakeRunner{results: []command.Result{
				resultForObservation(t, observation),
				resultForObservation(t, attainedObservationFor(target)),
			}}
			report, err := New(Config{
				Timeout:      time.Second,
				PollInterval: time.Millisecond,
			}, runner).VerifyRecoveryTarget(
				context.Background(),
				model.RunningPostgres{ConnString: "postgresql://verify"},
				target,
			)
			if err != nil {
				t.Fatalf("VerifyRecoveryTarget() error = %v", err)
			}
			if runner.calls != 1 || report.Checks[0].Status != model.CheckStatusFailed {
				t.Fatalf("fatal mismatch was polled: report=%#v calls=%d", report, runner.calls)
			}
		})
	}
}

func TestVerifierStopsAtOverallTimeoutWithLastEvidence(t *testing.T) {
	target := model.RecoveryTarget{Type: model.RecoveryTargetImmediate}
	transient := observationFor(target, "pause")
	transient.InRecovery = true
	transient.ReplayPaused = true
	transient.ReplayPauseState = "pause requested"
	runner := &fakeRunner{result: resultForObservation(t, transient)}

	report, err := New(Config{
		Timeout:      15 * time.Millisecond,
		PollInterval: time.Millisecond,
	}, runner).VerifyRecoveryTarget(
		context.Background(),
		model.RunningPostgres{ConnString: "postgresql://verify"},
		target,
	)
	if err != nil {
		t.Fatalf("VerifyRecoveryTarget() error = %v", err)
	}
	if runner.calls < 1 ||
		len(report.Evidence) != 1 ||
		report.Checks[0].Status != model.CheckStatusFailed ||
		!strings.Contains(report.Checks[0].Message, "observation deadline") {
		t.Fatalf("timeout report = %#v, calls = %d", report, runner.calls)
	}
}

func TestContextFailureReportRetainsLastEvidence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	evidence := commandEvidence(successfulResult(nil).Evidence)
	report, done := contextFailureReport(
		ctx,
		&evidence,
		errors.New("recovery remains transient"),
	)
	if !done ||
		len(report.Evidence) != 1 ||
		report.Evidence[0].ID != evidence.ID ||
		!strings.Contains(report.Checks[0].Message, "recovery remains transient") {
		t.Fatalf("contextFailureReport() = %#v, %t", report, done)
	}
}

func TestVerifierEvaluatesOnlyCompleteRetainedEvidence(t *testing.T) {
	target := model.RecoveryTarget{Type: model.RecoveryTargetLatest}
	valid := resultForObservation(t, attainedObservationFor(target))
	tests := []struct {
		name   string
		mutate func(*command.Result)
		want   string
	}{
		{
			name: "truncated retained stdout",
			mutate: func(result *command.Result) {
				result.Evidence.StdoutTruncated = true
			},
			want: "truncated",
		},
		{
			name: "raw differs from retained stdout",
			mutate: func(result *command.Result) {
				result.Evidence.Stdout = `{"schema_version":"wrong"}`
			},
			want: "parse recovery target observation",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := valid
			test.mutate(&result)
			report, err := New(Config{}, &fakeRunner{result: result}).VerifyRecoveryTarget(
				context.Background(),
				model.RunningPostgres{ConnString: "postgresql://verify"},
				target,
			)
			if err != nil {
				t.Fatalf("VerifyRecoveryTarget() error = %v", err)
			}
			if report.Checks[0].Status != model.CheckStatusFailed ||
				!strings.Contains(report.Checks[0].Message, test.want) {
				t.Fatalf("report = %#v, want message %q", report, test.want)
			}
		})
	}
}

func TestVerifierFailsClosedOnInvalidOrFailedObservation(t *testing.T) {
	target := model.RecoveryTarget{Type: model.RecoveryTargetLatest}
	tests := []struct {
		name   string
		pg     model.RunningPostgres
		result command.Result
		err    error
	}{
		{
			name: "missing connection",
		},
		{
			name:   "malformed output",
			pg:     model.RunningPostgres{ConnString: "postgresql://verify"},
			result: successfulResult([]byte(`{"schema_version":"wrong"}`)),
		},
		{
			name: "nonzero command",
			pg:   model.RunningPostgres{ConnString: "postgresql://verify"},
			result: func() command.Result {
				result := successfulResult(nil)
				result.Evidence.ExitStatus.Success = false
				result.Evidence.ExitStatus.ExitCode = 2
				result.Evidence.Stderr = "postgres failed"
				return result
			}(),
		},
		{
			name:   "runner error",
			pg:     model.RunningPostgres{ConnString: "postgresql://verify"},
			result: successfulResult(nil),
			err:    errors.New("runner failed"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := New(Config{}, &fakeRunner{
				result: test.result,
				err:    test.err,
			}).VerifyRecoveryTarget(context.Background(), test.pg, target)
			if err != nil {
				t.Fatalf("VerifyRecoveryTarget() error = %v", err)
			}
			if len(report.Checks) != 1 ||
				report.Checks[0].Status != model.CheckStatusFailed {
				t.Fatalf("report = %#v", report)
			}
			if err := ValidateReport(target, report); err == nil {
				t.Fatal("ValidateReport() accepted failed proof")
			}
		})
	}
}

func TestValidateReportRejectsEveryStructuralViolation(t *testing.T) {
	target := model.RecoveryTarget{Type: model.RecoveryTargetImmediate}
	base := validReport(t, target, attainedObservationFor(target))
	tests := []struct {
		name   string
		mutate func(*model.CheckReport)
	}{
		{
			name: "check cardinality",
			mutate: func(value *model.CheckReport) {
				value.Checks = nil
			},
		},
		{
			name: "check name",
			mutate: func(value *model.CheckReport) {
				value.Checks[0].Name = "other"
			},
		},
		{
			name: "invalid check",
			mutate: func(value *model.CheckReport) {
				value.Checks[0].Status = "future"
			},
		},
		{
			name: "failed check",
			mutate: func(value *model.CheckReport) {
				value.Checks[0].Status = model.CheckStatusFailed
			},
		},
		{
			name: "evidence reference cardinality",
			mutate: func(value *model.CheckReport) {
				value.Checks[0].EvidenceIDs = append(
					value.Checks[0].EvidenceIDs,
					"extra",
				)
			},
		},
		{
			name: "invalid evidence",
			mutate: func(value *model.CheckReport) {
				value.Evidence[0].CollectedAt = time.Time{}
			},
		},
		{
			name: "wrong operation",
			mutate: func(value *model.CheckReport) {
				value.Evidence[0].Attributes["operation"] = "other"
			},
		},
		{
			name: "valid observation does not prove target",
			mutate: func(value *model.CheckReport) {
				observation := attainedObservationFor(target)
				observation.RecoveryTargetAction = "promote"
				payload, err := json.Marshal(observation)
				if err != nil {
					t.Fatal(err)
				}
				value.Evidence[0].Command.Stdout = string(payload)
				value.Evidence[0].Command.StdoutBytes = int64(len(payload))
			},
		},
		{
			name: "attribute cardinality",
			mutate: func(value *model.CheckReport) {
				value.Checks[0].Attributes["extra"] = "value"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := cloneReport(t, base)
			test.mutate(&report)
			if err := ValidateReport(target, report); err == nil {
				t.Fatalf("ValidateReport() accepted %#v", report)
			}
		})
	}
}

func TestValidatePersistedRejectsMissingOrDuplicateProof(t *testing.T) {
	target := model.RecoveryTarget{Type: model.RecoveryTargetLatest}
	report := validReport(t, target, attainedObservationFor(target))
	for _, checks := range [][]model.Check{
		nil,
		append(append([]model.Check{}, report.Checks...), report.Checks...),
	} {
		if err := ValidatePersisted(target, checks, report.Evidence); err == nil {
			t.Fatalf("ValidatePersisted() accepted proof checks %#v", checks)
		}
	}
}

func TestConfiguredTargetValidationRejectsMissingAndMalformedSettings(t *testing.T) {
	target := model.RecoveryTarget{Type: model.RecoveryTargetTimestamp, Value: "2026-07-20T01:02:03Z"}
	tests := []struct {
		name   string
		mutate func(*Observation)
	}{
		{
			name: "missing target",
			mutate: func(value *Observation) {
				value.RecoveryTargetTime = ""
			},
		},
		{
			name: "malformed inclusive",
			mutate: func(value *Observation) {
				value.RecoveryTargetInclusive = "perhaps"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := attainedObservationFor(target)
			test.mutate(&observation)
			if _, err := Evaluate(target, observation); err == nil {
				t.Fatalf("Evaluate() accepted %#v", observation)
			}
		})
	}
}

func TestRecoveryValueParsersRejectMalformedValues(t *testing.T) {
	targetTests := []struct {
		target     model.RecoveryTarget
		configured string
	}{
		{target: model.RecoveryTarget{Type: model.RecoveryTargetImmediate}, configured: "later"},
		{target: model.RecoveryTarget{Type: model.RecoveryTargetTimestamp, Value: "invalid"}, configured: "2026-07-20 01:02:03+00"},
		{target: model.RecoveryTarget{Type: model.RecoveryTargetTimestamp, Value: "2026-07-20T01:02:03Z"}, configured: "invalid"},
		{target: model.RecoveryTarget{Type: model.RecoveryTargetLSN, Value: "invalid"}, configured: "0/1"},
		{target: model.RecoveryTarget{Type: model.RecoveryTargetLSN, Value: "0/1"}, configured: "invalid"},
		{target: model.RecoveryTarget{Type: model.RecoveryTargetXID, Value: "invalid"}, configured: "1"},
		{target: model.RecoveryTarget{Type: model.RecoveryTargetXID, Value: "1"}, configured: "invalid"},
		{target: model.RecoveryTarget{Type: model.RecoveryTargetRestorePoint, Value: "expected"}, configured: "actual"},
		{target: model.RecoveryTarget{Type: "future"}, configured: "value"},
	}
	for _, test := range targetTests {
		if err := compareTargetValue(test.target, test.configured); err == nil {
			t.Fatalf("compareTargetValue(%#v, %q) succeeded", test.target, test.configured)
		}
	}

	for _, value := range []string{"", "/", "x/1", "1/x", "1/2/3"} {
		if _, err := parseLSN(value); err == nil {
			t.Fatalf("parseLSN(%q) succeeded", value)
		}
	}
	if _, err := parsePostgresTimestamp("invalid"); err == nil {
		t.Fatal("parsePostgresTimestamp() accepted invalid timestamp")
	}
	if _, err := parsePostgresBool("perhaps"); err == nil {
		t.Fatal("parsePostgresBool() accepted invalid boolean")
	}
}

func TestEvidenceAndMessageHelpersRemainBounded(t *testing.T) {
	evidence := commandEvidence(model.CommandEvidence{})
	if evidence.CollectedAt.IsZero() || evidence.ID == "" {
		t.Fatalf("commandEvidence() did not supply collection identity: %#v", evidence)
	}

	message := boundedMessage(
		"prefix: ",
		errors.New(strings.Repeat("x", model.MaxCheckMessageBytes)+"\xff"),
	)
	if len(message) > model.MaxCheckMessageBytes || strings.ToValidUTF8(message, "") != message {
		t.Fatalf("boundedMessage() returned invalid message of %d bytes", len(message))
	}
	if got := boundedMessage("prefix", nil); got != "prefix" {
		t.Fatalf("boundedMessage() = %q", got)
	}
	splitMessage := boundedMessage(
		"",
		errors.New(strings.Repeat("x", model.MaxCheckMessageBytes-1)+"\u20ac"),
	)
	if len(splitMessage) > model.MaxCheckMessageBytes ||
		strings.ToValidUTF8(splitMessage, "") != splitMessage {
		t.Fatalf("boundedMessage() split UTF-8 sequence: %q", splitMessage)
	}
	if effectivePollInterval(0) != defaultPollInterval {
		t.Fatalf("effectivePollInterval(0) != %s", defaultPollInterval)
	}
	if equalStringMap(map[string]string{"a": "1"}, map[string]string{}) {
		t.Fatal("equalStringMap() accepted different cardinality")
	}
}

func TestValidateReportRejectsTampering(t *testing.T) {
	target := model.RecoveryTarget{Type: model.RecoveryTargetImmediate}
	observation := attainedObservationFor(target)
	report := validReport(t, target, observation)

	tests := []struct {
		name   string
		mutate func(*model.CheckReport)
	}{
		{
			name: "attribute",
			mutate: func(value *model.CheckReport) {
				value.Checks[0].Attributes[RecoveryStateAttribute] = "promoted"
			},
		},
		{
			name: "evidence source",
			mutate: func(value *model.CheckReport) {
				value.Evidence[0].Source = "other"
			},
		},
		{
			name: "truncated observation",
			mutate: func(value *model.CheckReport) {
				value.Evidence[0].Command.StdoutTruncated = true
			},
		},
		{
			name: "observation",
			mutate: func(value *model.CheckReport) {
				value.Evidence[0].Command.Stdout = `{"schema_version":"future"}`
			},
		},
		{
			name: "reference",
			mutate: func(value *model.CheckReport) {
				value.Checks[0].EvidenceIDs[0] = "missing"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copy := cloneReport(t, report)
			test.mutate(&copy)
			if err := ValidateReport(target, copy); err == nil {
				t.Fatalf("ValidateReport() accepted tampered report: %#v", copy)
			}
		})
	}
}

func TestParseObservationRejectsUnknownAndDuplicateMembers(t *testing.T) {
	for _, input := range []string{
		`{"schema_version":"pgdrill.recovery-observation/v2","future":true}`,
		`{"schema_version":"pgdrill.recovery-observation/v2","in_recovery":false,"in_recovery":true}`,
		`{"schema_version":"pgdrill.recovery-observation/v1"}`,
		`null`,
	} {
		if _, err := ParseObservation([]byte(input)); err == nil {
			t.Fatalf("ParseObservation(%s) succeeded", input)
		}
	}
}

func observationFor(target model.RecoveryTarget, action string) Observation {
	target = target.Normalized()
	observation := Observation{
		SchemaVersion:           ObservationSchema,
		ReplayPauseState:        "not paused",
		RecoveryTargetTimeline:  "latest",
		RecoveryTargetInclusive: "on",
		RecoveryTargetAction:    action,
	}
	switch target.Type {
	case model.RecoveryTargetImmediate:
		observation.RecoveryTarget = "immediate"
	case model.RecoveryTargetTimestamp:
		timestamp, _ := target.Timestamp()
		observation.RecoveryTargetTime = timestamp.UTC().Format("2006-01-02 15:04:05.999999999-07:00")
	case model.RecoveryTargetLSN:
		observation.RecoveryTargetLSN = target.Value
	case model.RecoveryTargetXID:
		observation.RecoveryTargetXID = target.Value
	case model.RecoveryTargetRestorePoint:
		observation.RecoveryTargetName = target.Value
	}
	if target.Timeline != "" {
		observation.RecoveryTargetTimeline = target.Timeline
	}
	if target.Inclusive != nil && !*target.Inclusive {
		observation.RecoveryTargetInclusive = "off"
	}
	return observation
}

func attainedObservationFor(target model.RecoveryTarget) Observation {
	observation := observationFor(target, "pause")
	if target.Normalized().Type != model.RecoveryTargetLatest {
		observation.InRecovery = true
		observation.ReplayPaused = true
		observation.ReplayPauseState = "paused"
	}
	return observation
}

func resultForObservation(t *testing.T, observation Observation) command.Result {
	t.Helper()
	payload, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	return successfulResult(payload)
}

func validReport(
	t *testing.T,
	target model.RecoveryTarget,
	observation Observation,
) model.CheckReport {
	t.Helper()
	payload, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	result := successfulResult(payload)
	evidence := commandEvidence(result.Evidence)
	state, err := Evaluate(target, observation)
	if err != nil {
		t.Fatal(err)
	}
	return model.CheckReport{
		Checks: []model.Check{{
			Name:        CheckName,
			Status:      model.CheckStatusPassed,
			Message:     "proof",
			EvidenceIDs: []string{evidence.ID},
			Attributes:  proofAttributes(target.Normalized(), observation, state),
		}},
		Evidence: []model.EvidenceRecord{evidence},
	}
}

func successfulResult(stdout []byte) command.Result {
	finishedAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	return command.Result{
		Raw: command.RawEvidence{Stdout: append([]byte(nil), stdout...)},
		Evidence: model.CommandEvidence{
			Path:           "psql",
			StartedAt:      finishedAt.Add(-time.Second),
			FinishedAt:     finishedAt,
			DurationMillis: 1000,
			ExitStatus: model.ExitStatus{
				Started:  true,
				Exited:   true,
				Success:  true,
				ExitCode: 0,
			},
			Stdout:      string(stdout),
			StdoutBytes: int64(len(stdout)),
		},
	}
}

func cloneReport(t *testing.T, report model.CheckReport) model.CheckReport {
	t.Helper()
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var cloned model.CheckReport
	if err := json.Unmarshal(payload, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func boolPointer(value bool) *bool {
	return &value
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

type fakeRunner struct {
	invocation  command.Invocation
	invocations []command.Invocation
	result      command.Result
	results     []command.Result
	err         error
	errs        []error
	calls       int
}

func (r *fakeRunner) Run(
	_ context.Context,
	invocation command.Invocation,
) (command.Result, error) {
	r.invocation = invocation
	r.invocations = append(r.invocations, invocation)
	index := r.calls
	r.calls++

	result := r.result
	if len(r.results) > 0 {
		if index >= len(r.results) {
			index = len(r.results) - 1
		}
		result = r.results[index]
	}
	err := r.err
	if len(r.errs) > 0 {
		errIndex := index
		if errIndex >= len(r.errs) {
			errIndex = len(r.errs) - 1
		}
		err = r.errs[errIndex]
	}
	return result, err
}
