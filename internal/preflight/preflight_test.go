package preflight

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestCheckerCollectsAllToolVersionsAndFailures(t *testing.T) {
	finishedAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	runner := &stubRunner{responses: []stubResponse{
		{result: commandResult("wal-g", "/opt/bin/wal-g", "WAL-G v3.0.7\n", "", model.ExitStatus{Started: true, Exited: true, Success: true, ExitCode: 0}, finishedAt)},
		{result: commandResult("postgres", "/opt/bin/postgres", "", "postgres: bad executable\n", model.ExitStatus{Started: true, Exited: true, ExitCode: 2}, finishedAt)},
	}}
	checker := NewChecker(runner, 3*time.Second)
	checker.now = func() time.Time { return finishedAt }

	result, err := checker.Run(context.Background(), []Requirement{
		{Tool: model.ToolWALG, Components: []string{"provider.wal-g"}, Binary: "wal-g", Args: []string{"--version"}},
		{Tool: model.ToolPostgres, Components: []string{"target.local"}, Binary: "postgres", Args: []string{"--version"}},
	})

	if err != nil {
		t.Fatalf("run checker: %v", err)
	}
	if result.SchemaVersion != CurrentSchemaVersion || result.Status != model.DrillStatusFailed {
		t.Fatalf("unexpected result %#v", result)
	}
	if len(result.Checks) != 2 || len(result.Evidence) != 2 || FailedCount(result) != 1 {
		t.Fatalf("unexpected checks/evidence %#v", result)
	}
	if result.Checks[0].Status != model.CheckStatusPassed || result.Checks[0].Attributes["version"] != "WAL-G v3.0.7" {
		t.Fatalf("unexpected WAL-G check %#v", result.Checks[0])
	}
	if result.Checks[1].Status != model.CheckStatusFailed || !strings.Contains(result.Checks[1].Message, "exit code 2") {
		t.Fatalf("unexpected postgres check %#v", result.Checks[1])
	}
	if got := result.Checks[0].Attributes["resolved_path"]; got != "/opt/bin/wal-g" {
		t.Fatalf("unexpected resolved path %q", got)
	}
	for _, call := range runner.calls {
		if call.Timeout != 3*time.Second {
			t.Fatalf("unexpected timeout %s", call.Timeout)
		}
	}
}

func TestCheckerReportsStartErrorsAndContinues(t *testing.T) {
	finishedAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	runner := &stubRunner{responses: []stubResponse{
		{
			result: commandResult("missing", "missing", "", "", model.ExitStatus{ExitCode: -1, Error: "executable file not found"}, finishedAt),
			err:    errors.New("executable file not found"),
		},
		{result: commandResult("psql", "/opt/bin/psql", "psql (PostgreSQL) 17.5\n", "", model.ExitStatus{Started: true, Exited: true, Success: true, ExitCode: 0}, finishedAt)},
	}}

	result, err := NewChecker(runner, 0).Run(context.Background(), []Requirement{
		{Tool: model.ToolWALG, Binary: "missing"},
		{Tool: model.ToolPSQL, Binary: "psql"},
	})

	if err != nil {
		t.Fatalf("run checker: %v", err)
	}
	if result.Status != model.DrillStatusFailed || len(result.Checks) != 2 {
		t.Fatalf("unexpected result %#v", result)
	}
	if result.Checks[0].Status != model.CheckStatusFailed || result.Checks[1].Status != model.CheckStatusPassed {
		t.Fatalf("checker did not collect all outcomes %#v", result.Checks)
	}
	if runner.calls[0].Timeout != DefaultTimeout {
		t.Fatalf("unexpected default timeout %s", runner.calls[0].Timeout)
	}
}

func TestCheckerReturnsPartialAbortedResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &stubRunner{}

	result, err := NewChecker(runner, 0).Run(ctx, []Requirement{{Tool: model.ToolWALG, Binary: "wal-g"}})

	if !errors.Is(err, context.Canceled) || result.Status != model.DrillStatusAborted {
		t.Fatalf("expected aborted result, got result=%#v err=%v", result, err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("canceled checker must not start tools: %#v", runner.calls)
	}
}

func TestVersionTextParsesKubectlClientJSON(t *testing.T) {
	got := versionText(model.ToolKubectl, `{"clientVersion":{"gitVersion":"v1.34.1"}}`, "")
	if got != "v1.34.1" {
		t.Fatalf("unexpected kubectl version %q", got)
	}
}

func TestVersionCheckPrefersCaptureErrorOverSuccessfulExit(t *testing.T) {
	evidence := model.CommandEvidence{
		Path:       "wal-g",
		ExitStatus: model.ExitStatus{Started: true, Exited: true, Success: true, ExitCode: 0},
		Stdout:     "large version output",
	}
	check := versionCheck(Requirement{Tool: model.ToolWALG}, evidence, "evidence-1", errors.New("command output exceeded limit"))
	if check.Status != model.CheckStatusFailed || !strings.Contains(check.Message, "command output exceeded limit") {
		t.Fatalf("unexpected capture failure check %#v", check)
	}
}

func TestSuiteExposesCanonicalCheckReport(t *testing.T) {
	finishedAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	runner := &stubRunner{responses: []stubResponse{{
		result: commandResult("wal-g", "/opt/bin/wal-g", "WAL-G v3.0.7\n", "", model.ExitStatus{Started: true, Exited: true, Success: true, ExitCode: 0}, finishedAt),
	}}}

	report, err := NewSuite([]Requirement{{Tool: model.ToolWALG, Binary: "wal-g", Args: []string{"--version"}}}, runner, time.Second).Check(context.Background())

	if err != nil {
		t.Fatalf("run suite: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusPassed || len(report.Evidence) != 1 {
		t.Fatalf("unexpected check report %#v", report)
	}
}

func TestMergeRequirementsKeepsDistinctBinaries(t *testing.T) {
	merged := mergeRequirements([]Requirement{
		{Tool: model.ToolPSQL, Components: []string{"probe.one"}, Binary: "/v16/psql", Args: []string{"--version"}},
		{Tool: model.ToolPSQL, Components: []string{"probe.two"}, Binary: "/v17/psql", Args: []string{"--version"}},
		{Tool: model.ToolPSQL, Components: []string{"probe.three"}, Binary: "/v16/psql", Args: []string{"--version"}},
	})
	if len(merged) != 2 {
		t.Fatalf("unexpected merged requirements %#v", merged)
	}
	if want := []string{"probe.one", "probe.three"}; !reflect.DeepEqual(merged[0].Components, want) {
		t.Fatalf("unexpected merged components: got %#v want %#v", merged[0].Components, want)
	}
}

type stubResponse struct {
	result command.Result
	err    error
}

type stubRunner struct {
	responses []stubResponse
	calls     []command.Invocation
}

func (r *stubRunner) Run(_ context.Context, invocation command.Invocation) (command.Result, error) {
	r.calls = append(r.calls, invocation)
	if len(r.responses) == 0 {
		return command.Result{}, errors.New("unexpected invocation")
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	return response.result, response.err
}

func commandResult(path, resolvedPath, stdout, stderr string, status model.ExitStatus, finishedAt time.Time) command.Result {
	return command.Result{Evidence: model.CommandEvidence{
		Path:         path,
		ResolvedPath: resolvedPath,
		FinishedAt:   finishedAt,
		ExitStatus:   status,
		Stdout:       stdout,
		Stderr:       stderr,
	}}
}
