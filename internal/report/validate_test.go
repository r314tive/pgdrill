package report

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/policy"
	"github.com/r314tive/pgdrill/internal/recoveryproof"
	"github.com/r314tive/pgdrill/internal/runspec"
)

func TestValidateRejectsMalformedCurrentReports(t *testing.T) {
	evidence := model.EvidenceRecord{
		ID:          "evidence-1",
		Kind:        model.EvidenceCheck,
		Source:      "test",
		CollectedAt: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		name   string
		mutate func(*model.DrillResult)
		want   string
	}{
		{name: "missing id", mutate: func(result *model.DrillResult) { result.ID = "" }, want: "id is required"},
		{name: "id control", mutate: func(result *model.DrillResult) { result.ID = "run\n1" }, want: "control characters"},
		{name: "attempt whitespace", mutate: func(result *model.DrillResult) { result.AttemptID = " attempt-1" }, want: "attempt_id"},
		{name: "invalid spec digest", mutate: func(result *model.DrillResult) { result.SpecDigest = "md5:no" }, want: "spec_digest must be a sha256"},
		{name: "missing spec", mutate: func(result *model.DrillResult) { result.Spec = nil }, want: "spec is required when spec_digest"},
		{name: "digest mismatch", mutate: func(result *model.DrillResult) { result.SpecDigest = "sha256:" + strings.Repeat("f", 64) }, want: "does not match spec digest"},
		{name: "mutated spec", mutate: func(result *model.DrillResult) { result.Spec.Source.Ref.Revision = "sha256:" + strings.Repeat("f", 64) }, want: "does not match spec digest"},
		{name: "unknown provider", mutate: func(result *model.DrillResult) { result.Provider = "future" }, want: "unsupported provider"},
		{name: "unknown target", mutate: func(result *model.DrillResult) { result.Target.Type = "future" }, want: "unsupported target type"},
		{name: "unknown recovery target", mutate: func(result *model.DrillResult) { result.RecoveryTarget.Type = "future" }, want: "unsupported recovery_target type"},
		{name: "missing started at", mutate: func(result *model.DrillResult) { result.StartedAt = time.Time{} }, want: "started_at is required"},
		{name: "reversed timestamps", mutate: func(result *model.DrillResult) { result.FinishedAt = result.StartedAt.Add(-time.Second) }, want: "finished_at must not be earlier"},
		{name: "unknown status", mutate: func(result *model.DrillResult) { result.Status = model.DrillStatusUnknown }, want: "unsupported terminal status"},
		{name: "backup id mismatch", mutate: func(result *model.DrillResult) { result.Backup.ID = "wal-g:other" }, want: "provider-scoped id"},
		{name: "backup provider id whitespace", mutate: func(result *model.DrillResult) { result.Backup.ProviderID = " base_1" }, want: "provider_id must not contain"},
		{name: "invalid backup lsn", mutate: func(result *model.DrillResult) { result.Backup.WALRange.StartLSN = "decimal" }, want: "invalid wal_range.start_lsn"},
		{name: "too many evidence records", mutate: func(result *model.DrillResult) {
			result.Evidence = make([]model.EvidenceRecord, model.MaxEvidenceRecordsPerReport+1)
		}, want: "evidence exceeds maximum count"},
		{name: "too many checks", mutate: func(result *model.DrillResult) {
			result.Checks = make([]model.Check, model.MaxChecksPerReport+1)
		}, want: "checks exceed maximum count"},
		{name: "too many operations", mutate: func(result *model.DrillResult) {
			result.Operations = make([]model.OperationCheckpoint, model.MaxOperationsPerReport+1)
		}, want: "operations exceed maximum count"},
		{name: "duplicate evidence", mutate: func(result *model.DrillResult) { result.Evidence = []model.EvidenceRecord{evidence, evidence} }, want: "duplicate evidence id"},
		{name: "missing evidence reference", mutate: func(result *model.DrillResult) {
			result.Checks = []model.Check{{Name: "sql", Status: model.CheckStatusPassed, EvidenceIDs: []string{"missing"}}}
		}, want: "references missing evidence"},
		{name: "unknown probe", mutate: func(result *model.DrillResult) {
			result.Checks = []model.Check{{Name: "sql", Probe: "future", Status: model.CheckStatusPassed}}
		}, want: "unsupported probe"},
		{name: "passed with failed check", mutate: func(result *model.DrillResult) {
			result.Checks = []model.Check{{Name: "sql", Status: model.CheckStatusFailed}}
		}, want: "passed report contains failed check"},
		{name: "passed with failure", mutate: func(result *model.DrillResult) {
			result.Failure = &model.DrillFailure{Stage: model.DrillStageProbeExecution, Message: "failed"}
		}, want: "passed report must not contain failure"},
		{name: "unknown failure stage", mutate: func(result *model.DrillResult) {
			result.Status = model.DrillStatusFailed
			result.Failure = &model.DrillFailure{Stage: "future", Message: "failed"}
		}, want: "unsupported stage"},
		{name: "command without payload", mutate: func(result *model.DrillResult) {
			result.Evidence = []model.EvidenceRecord{{ID: "command", Kind: model.EvidenceCommand, Source: "test", CollectedAt: evidence.CollectedAt}}
		}, want: "command evidence payload is required"},
		{name: "inconsistent command success", mutate: func(result *model.DrillResult) {
			result.Evidence = []model.EvidenceRecord{{
				ID:          "command",
				Kind:        model.EvidenceCommand,
				Source:      "test",
				CollectedAt: evidence.CollectedAt,
				Command: &model.CommandEvidence{
					Path:           "tool",
					StartedAt:      evidence.CollectedAt.Add(-time.Second),
					FinishedAt:     evidence.CollectedAt,
					DurationMillis: 1000,
					ExitStatus:     model.ExitStatus{Success: true},
				},
			}}
		}, want: "successful exit status is internally inconsistent"},
		{name: "oversized command stdout", mutate: func(result *model.DrillResult) {
			result.Evidence = []model.EvidenceRecord{validCommandEvidence(
				evidence.CollectedAt,
				strings.Repeat("x", model.MaxCommandEvidenceBytes+1),
			)}
		}, want: "stdout exceeds maximum evidence size"},
		{name: "invalid command stdout utf8", mutate: func(result *model.DrillResult) {
			result.Evidence = []model.EvidenceRecord{validCommandEvidence(
				evidence.CollectedAt,
				string([]byte{0xff}),
			)}
		}, want: "stdout must be valid UTF-8"},
		{name: "too many command args", mutate: func(result *model.DrillResult) {
			record := validCommandEvidence(evidence.CollectedAt, "")
			record.Command.Args = make([]string, model.MaxCommandArguments+1)
			result.Evidence = []model.EvidenceRecord{record}
		}, want: "args exceed maximum count"},
		{name: "too many command env entries", mutate: func(result *model.DrillResult) {
			record := validCommandEvidence(evidence.CollectedAt, "")
			record.Command.Env = make(map[string]string, model.MaxCommandEnvironmentEntries+1)
			for index := 0; index <= model.MaxCommandEnvironmentEntries; index++ {
				record.Command.Env[fmt.Sprintf("KEY_%d", index)] = "value"
			}
			result.Evidence = []model.EvidenceRecord{record}
		}, want: "env exceeds maximum count"},
		{name: "invalid command path utf8", mutate: func(result *model.DrillResult) {
			record := validCommandEvidence(evidence.CollectedAt, "")
			record.Command.Path = string([]byte{0xff})
			result.Evidence = []model.EvidenceRecord{record}
		}, want: "path must be valid UTF-8"},
		{name: "oversized command argument", mutate: func(result *model.DrillResult) {
			record := validCommandEvidence(evidence.CollectedAt, "")
			record.Command.Args = []string{strings.Repeat("x", model.MaxCommandArgumentBytes+1)}
			result.Evidence = []model.EvidenceRecord{record}
		}, want: "arg 0 exceeds maximum size"},
		{name: "invalid command environment name", mutate: func(result *model.DrillResult) {
			record := validCommandEvidence(evidence.CollectedAt, "")
			record.Command.Env = map[string]string{"BAD=NAME": "value"}
			result.Evidence = []model.EvidenceRecord{record}
		}, want: "env name"},
		{name: "oversized command environment value", mutate: func(result *model.DrillResult) {
			record := validCommandEvidence(evidence.CollectedAt, "")
			record.Command.Env = map[string]string{
				"NAME": strings.Repeat("x", model.MaxCommandEnvironmentValueBytes+1),
			}
			result.Evidence = []model.EvidenceRecord{record}
		}, want: "value exceeds maximum size"},
		{name: "oversized command exit error", mutate: func(result *model.DrillResult) {
			record := validCommandEvidence(evidence.CollectedAt, "")
			record.Command.ExitStatus = model.ExitStatus{
				Started:  true,
				Exited:   true,
				ExitCode: 1,
				Error:    strings.Repeat("x", model.MaxCommandErrorBytes+1),
			}
			result.Evidence = []model.EvidenceRecord{record}
		}, want: "exit status error exceeds maximum size"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validTestResult()
			tt.mutate(&result)
			err := Validate(result)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestValidateAllowsLegacyFailureWithoutDetails(t *testing.T) {
	result := validTestResult()
	result.Status = model.DrillStatusFailed
	if err := Validate(result); err != nil {
		t.Fatalf("validate legacy failure: %v", err)
	}
}

func TestValidateRequiresCoherentRecoveryPolicyEvaluation(t *testing.T) {
	recoveryPolicy := model.RecoveryPolicy{MaximumRTO: "10m"}
	result := testResultWithPolicy(t, recoveryPolicy, time.Time{})

	t.Run("missing", func(t *testing.T) {
		broken := result
		broken.PolicyEvaluation = nil
		if err := Validate(broken); err == nil || !strings.Contains(err.Error(), "policy_evaluation is required") {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("blocking passed report", func(t *testing.T) {
		if verdict := result.PolicyEvaluation.BlockingVerdicts(); len(verdict) != 1 || verdict[0].Status != model.PolicyVerdictUnknown {
			t.Fatalf("unexpected blocking verdicts %#v", verdict)
		}
		if err := Validate(result); err == nil || !strings.Contains(err.Error(), "passed report contains blocking policy verdict") {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("limit drift", func(t *testing.T) {
		passing := testResultWithPolicy(t, recoveryPolicy, result.StartedAt.Add(30*time.Second))
		changed := int64((9 * time.Minute).Milliseconds())
		passing.PolicyEvaluation.Verdicts[0].LimitMillis = &changed
		if err := Validate(passing); err == nil || !strings.Contains(err.Error(), "does not match spec policy") {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("evaluation after finish", func(t *testing.T) {
		passing := testResultWithPolicy(t, recoveryPolicy, result.StartedAt.Add(30*time.Second))
		passing.PolicyEvaluation.EvaluatedAt = passing.FinishedAt.Add(time.Second)
		if err := Validate(passing); err == nil || !strings.Contains(err.Error(), "must not be later than finished_at") {
			t.Fatalf("Validate() error = %v", err)
		}
	})
}

func TestValidateProducedRequiresNativeBackupAndProbeProof(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*model.DrillResult)
		want   string
	}{
		{
			name: "missing native backup",
			mutate: func(result *model.DrillResult) {
				result.Backup = model.Backup{}
			},
			want: "backup is required for a passed report",
		},
		{
			name: "missing checks",
			mutate: func(result *model.DrillResult) {
				result.Checks = nil
			},
			want: `does not prove required probe "select_1"`,
		},
		{
			name: "passing probe without evidence",
			mutate: func(result *model.DrillResult) {
				result.Checks[0].EvidenceIDs = nil
			},
			want: "has no evidence references",
		},
		{
			name: "missing binding",
			mutate: func(result *model.DrillResult) {
				result.Checks[0].Attributes = nil
			},
			want: `missing attribute "probe_name"`,
		},
		{
			name: "undeclared binding",
			mutate: func(result *model.DrillResult) {
				result.Checks[0].Attributes = map[string]string{model.ProbeNameAttribute: "other-query"}
			},
			want: "not declared by the drill spec",
		},
		{
			name: "no passing proof",
			mutate: func(result *model.DrillResult) {
				result.Checks[0].Status = model.CheckStatusWarning
			},
			want: `does not prove required probe "select_1"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validTestResult()
			test.mutate(&result)
			err := ValidateProduced(result)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateProduced() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateProducedRequiresAuthenticRecoveryTargetProof(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*model.DrillResult)
		want   string
	}{
		{
			name: "missing proof",
			mutate: func(result *model.DrillResult) {
				checks := result.Checks[:0]
				for _, check := range result.Checks {
					if check.Name != recoveryproof.CheckName {
						checks = append(checks, check)
					}
				}
				result.Checks = checks
			},
			want: "requires exactly one recovery target proof check",
		},
		{
			name: "tampered observation",
			mutate: func(result *model.DrillResult) {
				for index := range result.Evidence {
					record := &result.Evidence[index]
					if record.Source != recoveryproof.EvidenceSource {
						continue
					}
					record.Command.Stdout = strings.Replace(
						record.Command.Stdout,
						`"in_recovery":false`,
						`"in_recovery":true`,
						1,
					)
					record.Command.StdoutBytes = int64(len(record.Command.Stdout))
					return
				}
				t.Fatal("test result has no recovery target proof evidence")
			},
			want: "latest recovery is still in progress",
		},
		{
			name: "tampered proof attributes",
			mutate: func(result *model.DrillResult) {
				for index := range result.Checks {
					if result.Checks[index].Name == recoveryproof.CheckName {
						result.Checks[index].Attributes[recoveryproof.RecoveryStateAttribute] = "paused_at_target"
						return
					}
				}
				t.Fatal("test result has no recovery target proof check")
			},
			want: "attributes do not match retained observation",
		},
		{
			name: "truncated observation",
			mutate: func(result *model.DrillResult) {
				for index := range result.Evidence {
					if result.Evidence[index].Source == recoveryproof.EvidenceSource {
						result.Evidence[index].Command.StdoutTruncated = true
						return
					}
				}
				t.Fatal("test result has no recovery target proof evidence")
			},
			want: "requires complete successful command evidence",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validTestResult()
			test.mutate(&result)
			err := ValidateProduced(result)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateProduced() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateReaderContractAppliesCurrentButRetainsPreviousContract(t *testing.T) {
	result := validTestResult()
	checks := result.Checks[:0]
	for _, check := range result.Checks {
		if check.Name != recoveryproof.CheckName {
			checks = append(checks, check)
		}
	}
	result.Checks = checks

	if err := ValidateReaderContract(result); err == nil ||
		!strings.Contains(err.Error(), "requires exactly one recovery target proof check") {
		t.Fatalf("ValidateReaderContract(current without proof) error = %v", err)
	}

	result.SchemaVersion = model.PreviousReportSchemaVersion
	if err := ValidateReaderContract(result); err != nil {
		t.Fatalf("ValidateReaderContract(previous without v2 proof) error = %v", err)
	}
}

func TestValidateRequiredProbesRejectsSharedEvidence(t *testing.T) {
	result := validTestResult()
	result.Spec.ProbeProfile.Probes = append(
		result.Spec.ProbeProfile.Probes,
		model.ProbeDescriptor{Type: model.ProbePGDump, Name: "schema_dump"},
	)
	result.Checks = append(result.Checks, model.Check{
		Name:        "schema_dump",
		Probe:       model.ProbePGDump,
		Status:      model.CheckStatusPassed,
		EvidenceIDs: []string{"probe:select-1"},
		Attributes:  map[string]string{model.ProbeNameAttribute: "schema_dump"},
	})

	if err := validateRequiredProbes(result); err == nil ||
		!strings.Contains(err.Error(), "bound to another probe") {
		t.Fatalf("validateRequiredProbes() error = %v, want shared-evidence rejection", err)
	}
}

func TestValidateProducedRequiresCompleteSuccessfulOperationGraph(t *testing.T) {
	native := validTestResult()
	for index, checkpoint := range native.Operations {
		t.Run("native without "+checkpoint.Operation.Name, func(t *testing.T) {
			result := native
			result.Operations = append([]model.OperationCheckpoint(nil), native.Operations...)
			result.Operations = append(result.Operations[:index], result.Operations[index+1:]...)
			if err := ValidateProduced(result); err == nil || !strings.Contains(err.Error(), "passed native report") {
				t.Fatalf("ValidateProduced() error = %v", err)
			}
		})
	}
	t.Run("native empty", func(t *testing.T) {
		result := native
		result.Operations = nil
		if err := ValidateProduced(result); err == nil || !strings.Contains(err.Error(), "passed native report") {
			t.Fatalf("ValidateProduced() error = %v", err)
		}
	})

	managed := validManagedTestResult()
	for index, checkpoint := range managed.Operations {
		t.Run("managed without "+checkpoint.Operation.Name, func(t *testing.T) {
			result := managed
			result.Operations = append([]model.OperationCheckpoint(nil), managed.Operations...)
			result.Operations = append(result.Operations[:index], result.Operations[index+1:]...)
			if err := ValidateProduced(result); err == nil || !strings.Contains(err.Error(), "passed managed report") {
				t.Fatalf("ValidateProduced() error = %v", err)
			}
		})
	}
}

func TestValidateProducedRequiresChronologicalRecoveryProof(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*model.DrillResult)
		want   string
	}{
		{
			name: "stale required probe evidence",
			mutate: func(result *model.DrillResult) {
				result.Evidence[0].CollectedAt = result.StartedAt.Add(3 * time.Second)
			},
			want: "collected before successful start completion",
		},
		{
			name: "overlapping operations",
			mutate: func(result *model.DrillResult) {
				result.Operations[1].StartedAt = result.Operations[0].UpdatedAt.Add(-time.Nanosecond)
			},
			want: "started before operation",
		},
		{
			name: "proof after cleanup start",
			mutate: func(result *model.DrillResult) {
				provenAt := result.Operations[len(result.Operations)-1].StartedAt.Add(time.Nanosecond)
				result.PolicyEvaluation.RecoveryProvenAt = &provenAt
			},
			want: "must not be later than cleanup start",
		},
		{
			name: "evaluation before cleanup completion",
			mutate: func(result *model.DrillResult) {
				result.PolicyEvaluation.EvaluatedAt = result.Operations[len(result.Operations)-1].UpdatedAt.Add(-time.Nanosecond)
			},
			want: "must not be earlier than cleanup completion",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validTestResult()
			test.mutate(&result)
			if err := ValidateProduced(result); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateProduced() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateProducedRejectsUnavailableOrMissingPassedBackup(t *testing.T) {
	for _, status := range []model.BackupStatus{
		model.BackupStatusUnknown,
		model.BackupStatusWaitingForWAL,
		model.BackupStatusRunning,
		model.BackupStatusFailed,
		model.BackupStatusInvalid,
	} {
		t.Run(string(status), func(t *testing.T) {
			result := validTestResult()
			result.Backup.Status = status
			if err := ValidateProduced(result); err == nil || !strings.Contains(err.Error(), "must be available") {
				t.Fatalf("ValidateProduced() error = %v", err)
			}
		})
	}

	managed := validManagedTestResult()
	managed.Backup = model.Backup{}
	if err := ValidateProduced(managed); err == nil || !strings.Contains(err.Error(), "backup is required for a passed report") {
		t.Fatalf("ValidateProduced(managed without backup) error = %v", err)
	}
}

func TestValidateProducedBindsRecoveryProofToStartAndProbeEvidence(t *testing.T) {
	tests := []struct {
		name     string
		provenAt time.Duration
	}{
		{name: "before postgres start completion", provenAt: 5 * time.Second},
		{name: "before required probe evidence", provenAt: 10 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validTestResult()
			provenAt := result.StartedAt.Add(test.provenAt)
			evaluation, err := policy.Evaluate(
				result.Spec.Policy,
				result.RecoveryTarget,
				policy.Facts{
					StartedAt:        result.StartedAt,
					EvaluatedAt:      result.FinishedAt,
					RecoveryProvenAt: provenAt,
					Backup:           result.Backup,
					Operations:       result.Operations,
				},
			)
			if err != nil {
				t.Fatalf("policy.Evaluate() error = %v", err)
			}
			result.PolicyEvaluation = &evaluation
			if err := ValidateProduced(result); err == nil || !strings.Contains(err.Error(), "must not precede retained start and probe proof") {
				t.Fatalf("ValidateProduced() error = %v", err)
			}
		})
	}
}

func TestValidateProducedRecomputesPolicyFactsWithoutMutatingMessages(t *testing.T) {
	result := testResultWithPolicy(
		t,
		model.RecoveryPolicy{MaximumRTO: "2m"},
		validTestResult().StartedAt.Add(30*time.Second),
	)
	originalMessage := result.PolicyEvaluation.Verdicts[0].Message
	if err := ValidateProduced(result); err != nil {
		t.Fatalf("ValidateProduced() error = %v", err)
	}
	if got := result.PolicyEvaluation.Verdicts[0].Message; got != originalMessage {
		t.Fatalf("validator mutated verdict message: got %q, want %q", got, originalMessage)
	}

	result.PolicyEvaluation.Verdicts[0].Message = "operator-facing wording may differ"
	if err := ValidateProduced(result); err != nil {
		t.Fatalf("ValidateProduced(message-only change) error = %v", err)
	}

	tampered := int64(time.Second.Milliseconds())
	result.PolicyEvaluation.Verdicts[0].ObservedMillis = &tampered
	if err := ValidateProduced(result); err == nil || !strings.Contains(err.Error(), "does not match report facts") {
		t.Fatalf("ValidateProduced(tampered observation) error = %v", err)
	}
}

func TestValidateProducedBoundsNestedTimestamps(t *testing.T) {
	operationResult := validTestResult()

	tests := []struct {
		name   string
		result model.DrillResult
		mutate func(*model.DrillResult)
		want   string
	}{
		{
			name:   "operation before report",
			result: operationResult,
			mutate: func(result *model.DrillResult) {
				result.Operations[0].StartedAt = result.StartedAt.Add(-time.Nanosecond)
			},
			want: "operation \"prepare-target\" started_at",
		},
		{
			name:   "operation after report",
			result: operationResult,
			mutate: func(result *model.DrillResult) {
				result.Operations[0].UpdatedAt = result.FinishedAt.Add(time.Nanosecond)
			},
			want: "operation \"prepare-target\" updated_at",
		},
		{
			name:   "evidence before report",
			result: validTestResult(),
			mutate: func(result *model.DrillResult) {
				result.Evidence = []model.EvidenceRecord{{
					ID:          "early",
					Kind:        model.EvidenceCheck,
					Source:      "test",
					CollectedAt: result.StartedAt.Add(-time.Nanosecond),
				}}
			},
			want: "collected_at must not be earlier",
		},
		{
			name:   "evidence after report",
			result: validTestResult(),
			mutate: func(result *model.DrillResult) {
				result.Evidence = []model.EvidenceRecord{{
					ID:          "late",
					Kind:        model.EvidenceCheck,
					Source:      "test",
					CollectedAt: result.FinishedAt.Add(time.Nanosecond),
				}}
			},
			want: "collected_at must not be later",
		},
		{
			name:   "command before report",
			result: validTestResult(),
			mutate: func(result *model.DrillResult) {
				record := validCommandEvidence(result.StartedAt.Add(time.Second), "")
				record.Command.StartedAt = result.StartedAt.Add(-time.Nanosecond)
				record.Command.DurationMillis = 1000
				result.Evidence = []model.EvidenceRecord{record}
			},
			want: "command started_at must not be earlier",
		},
		{
			name:   "command after collection",
			result: validTestResult(),
			mutate: func(result *model.DrillResult) {
				record := validCommandEvidence(result.StartedAt.Add(time.Second), "")
				record.Command.FinishedAt = record.CollectedAt.Add(time.Nanosecond)
				result.Evidence = []model.EvidenceRecord{record}
			},
			want: "command finished_at must not be later than evidence collected_at",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.result
			result.Operations = append([]model.OperationCheckpoint(nil), result.Operations...)
			test.mutate(&result)
			err := ValidateProduced(result)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateProduced() error = %v, want %q", err, test.want)
			}
		})
	}
}

func validCommandEvidence(collectedAt time.Time, stdout string) model.EvidenceRecord {
	return model.EvidenceRecord{
		ID:          "command",
		Kind:        model.EvidenceCommand,
		Source:      "test",
		CollectedAt: collectedAt,
		Command: &model.CommandEvidence{
			Path:           "tool",
			StartedAt:      collectedAt.Add(-time.Second),
			FinishedAt:     collectedAt,
			DurationMillis: 1000,
			ExitStatus: model.ExitStatus{
				Started:  true,
				Exited:   true,
				Success:  true,
				ExitCode: 0,
			},
			Stdout:      stdout,
			StdoutBytes: int64(len(stdout)),
		},
	}
}

func testResultWithPolicy(t *testing.T, recoveryPolicy model.RecoveryPolicy, recoveryProvenAt time.Time) model.DrillResult {
	t.Helper()
	result := validTestResult()
	document := *result.Spec
	document.Policy = recoveryPolicy
	spec, err := runspec.New(document)
	if err != nil {
		t.Fatalf("runspec.New() error = %v", err)
	}
	canonical := spec.Document()
	result.Spec = &canonical
	result.SpecDigest = spec.Digest()
	result.Operations = successfulNativeTestOperations(result)
	evaluation, err := policy.Evaluate(recoveryPolicy, result.RecoveryTarget, policy.Facts{
		StartedAt:        result.StartedAt,
		EvaluatedAt:      result.FinishedAt,
		RecoveryProvenAt: recoveryProvenAt,
		Backup:           result.Backup,
		Operations:       result.Operations,
	})
	if err != nil {
		t.Fatalf("policy.Evaluate() error = %v", err)
	}
	result.PolicyEvaluation = &evaluation
	return result
}

func TestValidateChecksOperationIdentityAndTerminalState(t *testing.T) {
	result := validTestResult()
	operation, err := model.NewOperation(model.AttemptIdentity{
		RunID:      result.ID,
		AttemptID:  result.AttemptID,
		SpecDigest: result.SpecDigest,
	}, model.DrillStageTargetPreparation, model.OperationTargetPrepare, "prepare-target", 0)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	now := result.StartedAt
	checkpoint := model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateSucceeded,
		StartedAt:     now,
		UpdatedAt:     now,
	}
	result.Operations = []model.OperationCheckpoint{checkpoint}
	if err := Validate(result); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	result.Operations[0].State = model.OperationStateFailed
	if err := Validate(result); err == nil || !strings.Contains(err.Error(), "passed report operation") {
		t.Fatalf("Validate(failed operation) error = %v", err)
	}
	result.Operations[0] = checkpoint
	result.Operations = append(result.Operations, checkpoint)
	if err := Validate(result); err == nil || !strings.Contains(err.Error(), "duplicate operation key") {
		t.Fatalf("Validate(duplicate operation) error = %v", err)
	}
}

func TestValidateChecksArtifactProvenance(t *testing.T) {
	result := validTestResult()
	metadata, err := model.NewArtifactMetadata("application/yaml", model.ArtifactRetentionHistory, model.ArtifactRedactionNotRequired)
	if err != nil {
		t.Fatalf("NewArtifactMetadata() error = %v", err)
	}
	ref, err := model.NewArtifactRef(
		"sha256:"+strings.Repeat("a", 64),
		"report.json.artifacts/sha256/aa/"+strings.Repeat("a", 64),
		1024,
		metadata,
	)
	if err != nil {
		t.Fatalf("NewArtifactRef() error = %v", err)
	}
	result.Artifacts = []model.ArtifactRef{ref}
	result.Evidence = append(result.Evidence, model.EvidenceRecord{
		ID:          "manifest",
		Kind:        model.EvidenceRuntime,
		Source:      "cnpg",
		CollectedAt: result.StartedAt,
		ArtifactIDs: []string{ref.ID},
	})
	if err := Validate(result); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	t.Run("missing", func(t *testing.T) {
		broken := result
		broken.Evidence = append([]model.EvidenceRecord(nil), result.Evidence...)
		broken.Evidence[len(broken.Evidence)-1].ArtifactIDs = []string{"sha256:" + strings.Repeat("b", 64)}
		if err := Validate(broken); err == nil || !strings.Contains(err.Error(), "references missing artifact") {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("orphan", func(t *testing.T) {
		broken := result
		broken.Evidence = append([]model.EvidenceRecord(nil), result.Evidence...)
		broken.Evidence[len(broken.Evidence)-1].ArtifactIDs = nil
		if err := Validate(broken); err == nil || !strings.Contains(err.Error(), "not referenced by evidence") {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		broken := result
		broken.Artifacts = []model.ArtifactRef{ref, ref}
		if err := Validate(broken); err == nil || !strings.Contains(err.Error(), "duplicate artifact id") {
			t.Fatalf("Validate() error = %v", err)
		}
	})
}

func TestJSONFileSinkRejectsNonTerminalOperation(t *testing.T) {
	result := validTestResult()
	operation, err := model.NewOperation(model.AttemptIdentity{
		RunID:      result.ID,
		AttemptID:  result.AttemptID,
		SpecDigest: result.SpecDigest,
	}, model.DrillStageTargetPreparation, model.OperationTargetPrepare, "prepare-target", 0)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	result.Status = model.DrillStatusFailed
	result.Failure = &model.DrillFailure{Stage: model.DrillStageTargetPreparation, Message: "executor lost"}
	result.Operations = []model.OperationCheckpoint{{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     result.StartedAt,
		UpdatedAt:     result.StartedAt,
	}}
	err = (JSONFileSink{Path: filepath.Join(t.TempDir(), "report.json")}).Write(context.Background(), result)
	if err == nil || !strings.Contains(err.Error(), "non-terminal state") {
		t.Fatalf("Write(intent operation) error = %v", err)
	}
}

func TestJSONFileSinkRejectsMissingFailureBeforeCreatingDirectory(t *testing.T) {
	result := validTestResult()
	result.Status = model.DrillStatusFailed
	reportDir := filepath.Join(t.TempDir(), "reports")
	err := (JSONFileSink{Path: filepath.Join(reportDir, "drill.json")}).Write(context.Background(), result)
	if err == nil || !strings.Contains(err.Error(), "failed report requires failure details") {
		t.Fatalf("expected producer failure validation error, got %v", err)
	}
	if _, statErr := os.Stat(reportDir); !os.IsNotExist(statErr) {
		t.Fatalf("invalid report created output directory: %v", statErr)
	}
}

func TestReadJSONRejectsBrokenEvidenceReference(t *testing.T) {
	result := validTestResult()
	result.Checks = []model.Check{{
		Name:        "sql",
		Status:      model.CheckStatusPassed,
		EvidenceIDs: []string{"missing"},
	}}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	_, err = ReadJSON(strings.NewReader(string(data)))
	if err == nil || !strings.Contains(err.Error(), "references missing evidence") {
		t.Fatalf("expected broken reference error, got %v", err)
	}
}
