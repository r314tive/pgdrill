package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

const (
	CurrentSchemaVersion = "pgdrill.doctor/v1alpha1"
	DefaultTimeout       = 15 * time.Second
)

type Result struct {
	SchemaVersion  string                  `json:"schema_version"`
	PGDrillVersion string                  `json:"pgdrill_version,omitempty"`
	Cluster        string                  `json:"cluster,omitempty"`
	Provider       model.ProviderType      `json:"provider,omitempty"`
	Target         model.RestoreTargetType `json:"target"`
	Status         model.DrillStatus       `json:"status"`
	StartedAt      time.Time               `json:"started_at"`
	FinishedAt     time.Time               `json:"finished_at"`
	Checks         []model.Check           `json:"checks"`
	Evidence       []model.EvidenceRecord  `json:"evidence"`
}

type Checker struct {
	Runner  command.Runner
	Timeout time.Duration
	now     func() time.Time
}

type Suite struct {
	checker      Checker
	requirements []Requirement
}

func NewChecker(runner command.Runner, timeout time.Duration) Checker {
	if runner == nil {
		runner = command.NewRunner(command.Options{})
	}
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	return Checker{Runner: runner, Timeout: timeout, now: time.Now}
}

func NewSuite(requirements []Requirement, runner command.Runner, timeout time.Duration) Suite {
	return Suite{
		checker:      NewChecker(runner, timeout),
		requirements: append([]Requirement{}, requirements...),
	}
}

func (s Suite) Check(ctx context.Context) (model.CheckReport, error) {
	result, err := s.checker.Run(ctx, s.requirements)
	return model.CheckReport{Checks: result.Checks, Evidence: result.Evidence}, err
}

func (c Checker) Run(ctx context.Context, requirements []Requirement) (Result, error) {
	now := c.now
	if now == nil {
		now = time.Now
	}
	result := Result{
		SchemaVersion: CurrentSchemaVersion,
		Status:        model.DrillStatusPassed,
		StartedAt:     now().UTC(),
		Checks:        make([]model.Check, 0, len(requirements)),
		Evidence:      make([]model.EvidenceRecord, 0, len(requirements)),
	}

	for index, requirement := range requirements {
		if err := ctx.Err(); err != nil {
			result.Status = model.DrillStatusAborted
			result.FinishedAt = now().UTC()
			return result, err
		}

		commandResult, runErr := c.Runner.Run(ctx, command.Invocation{
			Path:         requirement.Binary,
			Args:         append([]string{}, requirement.Args...),
			Env:          copyStringMap(requirement.Env),
			WorkDir:      requirement.WorkDir,
			Timeout:      c.effectiveTimeout(),
			RedactValues: append([]string{}, requirement.RedactValues...),
		})
		evidence := versionEvidence(requirement, index, commandResult.Evidence, now)
		check := versionCheck(requirement, commandResult.Evidence, evidence.ID, runErr)
		result.Evidence = append(result.Evidence, evidence)
		result.Checks = append(result.Checks, check)

		if ctx.Err() != nil || commandResult.Evidence.ExitStatus.Canceled {
			result.Status = model.DrillStatusAborted
			result.FinishedAt = now().UTC()
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			return result, context.Canceled
		}
		if check.Status == model.CheckStatusFailed {
			result.Status = model.DrillStatusFailed
		}
	}

	result.FinishedAt = now().UTC()
	return result, nil
}

func (c Checker) effectiveTimeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

func versionEvidence(requirement Requirement, index int, evidence model.CommandEvidence, now func() time.Time) model.EvidenceRecord {
	collectedAt := evidence.FinishedAt
	if collectedAt.IsZero() {
		collectedAt = now().UTC()
	}
	return model.EvidenceRecord{
		ID:          fmt.Sprintf("doctor:%s:%d:%s", requirement.Tool, index, collectedAt.Format(time.RFC3339Nano)),
		Kind:        model.EvidenceCommand,
		Source:      "doctor",
		CollectedAt: collectedAt,
		Command:     &evidence,
		Attributes: map[string]string{
			"components": strings.Join(requirement.Components, ","),
			"operation":  "version",
			"tool":       string(requirement.Tool),
		},
	}
}

func versionCheck(requirement Requirement, evidence model.CommandEvidence, evidenceID string, runErr error) model.Check {
	attributes := map[string]string{
		"binary":     evidence.Path,
		"components": strings.Join(requirement.Components, ","),
		"tool":       string(requirement.Tool),
	}
	if evidence.ResolvedPath != "" {
		attributes["resolved_path"] = evidence.ResolvedPath
	}
	version := versionText(requirement.Tool, evidence.Stdout, evidence.Stderr)
	if version != "" {
		attributes["version"] = version
	}

	check := model.Check{
		Name:        "tool." + string(requirement.Tool),
		Status:      model.CheckStatusPassed,
		EvidenceIDs: []string{evidenceID},
		Attributes:  attributes,
	}
	if runErr != nil || !evidence.ExitStatus.Success {
		check.Status = model.CheckStatusFailed
		if runErr != nil {
			check.Message = runErr.Error()
		} else {
			check.Message = evidence.ExitStatus.Summary()
		}
		if detail := firstOutputLine(evidence.Stderr, evidence.Stdout); detail != "" && !strings.Contains(check.Message, detail) {
			check.Message += ": " + detail
		}
		return check
	}
	if version == "" {
		check.Message = "version command succeeded"
	} else {
		check.Message = version
	}
	return check
}

func versionText(tool model.ToolType, stdout, stderr string) string {
	if tool == model.ToolKubectl {
		var output struct {
			ClientVersion struct {
				GitVersion string `json:"gitVersion"`
			} `json:"clientVersion"`
		}
		if json.Unmarshal([]byte(stdout), &output) == nil && output.ClientVersion.GitVersion != "" {
			return output.ClientVersion.GitVersion
		}
	}
	return firstOutputLine(stdout, stderr)
}

func firstOutputLine(values ...string) string {
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			line = strings.Join(strings.Fields(line), " ")
			if line == "" {
				continue
			}
			const limit = 512
			if len(line) > limit {
				return line[:limit] + "..."
			}
			return line
		}
	}
	return ""
}

func FailedCount(result Result) int {
	failed := 0
	for _, check := range result.Checks {
		if check.Status == model.CheckStatusFailed {
			failed++
		}
	}
	return failed
}

func IsInterrupted(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
