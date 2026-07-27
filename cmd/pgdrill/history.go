package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/r314tive/pgdrill/internal/core"
	"github.com/r314tive/pgdrill/internal/finalize"
	"github.com/r314tive/pgdrill/internal/history"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/report"
)

const defaultHistoryListLimit = 50

func historyEventSink(path string) core.EventSink {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	return history.DirectoryStore{Path: path}
}

func persistHistory(ctx context.Context, path string, result model.DrillResult) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	persistCtx, cancel := finalize.Context(ctx, 0)
	defer cancel()
	return (history.DirectoryStore{Path: path}).SaveReport(persistCtx, result)
}

func runHistory(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printHistoryUsage(stderr)
		return 2
	}
	switch args[0] {
	case "list":
		return runHistoryList(ctx, args[1:], stdout, stderr)
	case "show":
		return runHistoryShow(ctx, args[1:], stdout, stderr)
	case "import":
		return runHistoryImport(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printHistoryUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown history command %q\n\n", args[0])
		printHistoryUsage(stderr)
		return 2
	}
}

func runHistoryList(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("history list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var storePath string
	var format string
	var limit int
	fs.StringVar(&storePath, "store", "", "history store path")
	fs.StringVar(&format, "format", "text", "output format: text or json")
	fs.IntVar(&limit, "limit", defaultHistoryListLimit, "maximum attempts to print")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "history list does not accept positional arguments")
		return 2
	}
	if limit < 1 || limit > 1000 {
		fmt.Fprintln(stderr, "history list limit must be between 1 and 1000")
		return 2
	}
	path, err := resolveHistoryPath(storePath)
	if err != nil {
		fmt.Fprintf(stderr, "resolve history store: %v\n", err)
		return 1
	}
	summaries, err := (history.DirectoryStore{Path: path}).List(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "list history: %v\n", err)
		return 1
	}
	if len(summaries) > limit {
		summaries = summaries[:limit]
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "text":
		if err := writeHistoryListText(stdout, path, summaries); err != nil {
			fmt.Fprintf(stderr, "write history list: %v\n", err)
			return 1
		}
	case "json":
		output := struct {
			SchemaVersion string                   `json:"schema_version"`
			Store         string                   `json:"store"`
			Attempts      []history.AttemptSummary `json:"attempts"`
		}{
			SchemaVersion: history.CurrentViewSchemaVersion,
			Store:         path,
			Attempts:      summaries,
		}
		if err := writeIndentedJSON(stdout, output); err != nil {
			fmt.Fprintf(stderr, "write history list: %v\n", err)
			return 1
		}
	default:
		fmt.Fprintf(stderr, "unsupported format %q\n", format)
		return 2
	}
	return 0
}

func runHistoryShow(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("history show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var storePath string
	var attemptID string
	var format string
	fs.StringVar(&storePath, "store", "", "history store path")
	fs.StringVar(&attemptID, "attempt-id", "", "show one execution attempt")
	fs.StringVar(&format, "format", "text", "output format: text or json")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "history show requires exactly one run id")
		return 2
	}
	path, err := resolveHistoryPath(storePath)
	if err != nil {
		fmt.Fprintf(stderr, "resolve history store: %v\n", err)
		return 1
	}
	store := history.DirectoryStore{Path: path}
	var record history.RunRecord
	if attemptID == "" {
		record, err = store.Show(ctx, fs.Arg(0))
	} else {
		record, err = store.ShowAttempt(ctx, fs.Arg(0), attemptID)
	}
	if err != nil {
		fmt.Fprintf(stderr, "show history: %v\n", err)
		return 1
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "text":
		if err := writeHistoryShowText(stdout, path, record); err != nil {
			fmt.Fprintf(stderr, "write history show: %v\n", err)
			return 1
		}
	case "json":
		if err := writeIndentedJSON(stdout, record); err != nil {
			fmt.Fprintf(stderr, "write history show: %v\n", err)
			return 1
		}
	default:
		fmt.Fprintf(stderr, "unsupported format %q\n", format)
		return 2
	}
	return 0
}

func runHistoryImport(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("history import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var storePath string
	fs.StringVar(&storePath, "store", "", "history store path")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "history import requires exactly one report path")
		return 2
	}
	path, err := resolveHistoryPath(storePath)
	if err != nil {
		fmt.Fprintf(stderr, "resolve history store: %v\n", err)
		return 1
	}
	result, err := report.ReadJSONFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "import history report: %v\n", err)
		return 1
	}
	if err := (history.DirectoryStore{Path: path}).SaveReport(ctx, result); err != nil {
		fmt.Fprintf(stderr, "import history report: %v\n", err)
		return 1
	}
	fmt.Fprintf(
		stdout,
		"Imported run %s attempt %s (%s) into %s\n",
		oneLine(result.ID),
		oneLine(result.AttemptID),
		result.Status,
		oneLine(path),
	)
	return 0
}

func resolveHistoryPath(explicit string) (string, error) {
	if path := strings.TrimSpace(explicit); path != "" {
		return filepath.Clean(path), nil
	}
	if path := strings.TrimSpace(os.Getenv("PGDRILL_HISTORY_DIR")); path != "" {
		return filepath.Clean(path), nil
	}
	if stateHome := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); stateHome != "" {
		return filepath.Join(stateHome, "pgdrill", "history"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "pgdrill", "history"), nil
}

func writeHistoryListText(w io.Writer, path string, summaries []history.AttemptSummary) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintf(table, "Store:\t%s\nAttempts:\t%d\n", oneLine(path), len(summaries)); err != nil {
		return err
	}
	if len(summaries) == 0 {
		return table.Flush()
	}
	if _, err := fmt.Fprintln(table, "\nSTARTED\tSTATUS\tRUN ID\tATTEMPT ID\tFAILURE STAGE\tEVENTS\tREPORT"); err != nil {
		return err
	}
	for _, summary := range summaries {
		if _, err := fmt.Fprintf(
			table,
			"%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			formatHistoryTime(summary.StartedAt),
			summary.Status,
			oneLine(summary.RunID),
			oneLine(summary.AttemptID),
			valueOrDash(string(summary.FailureStage)),
			summary.EventCount,
			strconv.FormatBool(summary.ReportAvailable),
		); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writeHistoryShowText(w io.Writer, path string, record history.RunRecord) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"Store", path},
		{"Schema", record.SchemaVersion},
		{"Run ID", record.RunID},
		{"Spec digest", record.SpecDigest},
		{"Attempts", strconv.Itoa(len(record.Attempts))},
	}
	if record.Spec != nil {
		rows = append(rows,
			[2]string{"Mode", string(record.Spec.Mode)},
			[2]string{"Cluster", record.Spec.Cluster},
			[2]string{"Source", record.Spec.Source.Ref.ID},
			[2]string{"Target", record.Spec.Target.Ref.ID},
		)
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s:\t%s\n", row[0], oneLine(row[1])); err != nil {
			return err
		}
	}
	for _, attempt := range record.Attempts {
		if _, err := fmt.Fprintf(table, "\nAttempt:\t%s\n", oneLine(attempt.AttemptID)); err != nil {
			return err
		}
		if attempt.Report != nil {
			result := attempt.Report
			failureStage := ""
			failureMessage := ""
			if result.Failure != nil {
				failureStage = string(result.Failure.Stage)
				failureMessage = result.Failure.Message
			}
			attemptRows := [][2]string{
				{"Report", "true"},
				{"Status", string(result.Status)},
				{"Started", formatHistoryTime(result.StartedAt)},
				{"Finished", formatHistoryTime(result.FinishedAt)},
				{"Failure stage", valueOrDash(failureStage)},
				{"Failure", valueOrDash(oneLine(failureMessage))},
				{"Checks", strconv.Itoa(len(result.Checks))},
				{"Evidence", strconv.Itoa(len(result.Evidence))},
				{"Artifacts", strconv.Itoa(len(result.Artifacts))},
			}
			for _, row := range attemptRows {
				if _, err := fmt.Fprintf(table, "%s:\t%s\n", row[0], row[1]); err != nil {
					return err
				}
			}
			if len(result.Checks) > 0 {
				if _, err := fmt.Fprintln(table, "\nCHECK\tSTATUS\tPROBE\tMESSAGE"); err != nil {
					return err
				}
				for _, check := range result.Checks {
					if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", oneLine(check.Name), check.Status, valueOrDash(string(check.Probe)), oneLine(check.Message)); err != nil {
						return err
					}
				}
			}
			if result.PolicyEvaluation != nil {
				if _, err := fmt.Fprintln(table, "\nPOLICY\tSTATUS\tBASIS\tMESSAGE"); err != nil {
					return err
				}
				for _, verdict := range result.PolicyEvaluation.Verdicts {
					if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", verdict.Assertion, verdict.Status, verdict.Basis, oneLine(verdict.Message)); err != nil {
						return err
					}
				}
			}
			if len(result.Artifacts) > 0 {
				if _, err := fmt.Fprintln(table, "\nARTIFACT\tMEDIA TYPE\tBYTES\tURI"); err != nil {
					return err
				}
				for _, artifact := range result.Artifacts {
					if _, err := fmt.Fprintf(table, "%s\t%s\t%d\t%s\n", artifact.ID, oneLine(artifact.MediaType), artifact.SizeBytes, oneLine(artifact.URI)); err != nil {
						return err
					}
				}
			}
		} else {
			if _, err := fmt.Fprintln(table, "Report:\t-"); err != nil {
				return err
			}
		}
		if len(attempt.Events) > 0 {
			if _, err := fmt.Fprintln(table, "\nSEQ\tOCCURRED\tTYPE\tSTAGE\tOUTCOME/STATUS\tMESSAGE"); err != nil {
				return err
			}
			for _, event := range attempt.Events {
				outcome := string(event.Outcome)
				if event.Status != "" {
					outcome = string(event.Status)
				}
				if _, err := fmt.Fprintf(
					table,
					"%d\t%s\t%s\t%s\t%s\t%s\n",
					event.Sequence,
					formatHistoryTime(event.OccurredAt),
					event.Type,
					valueOrDash(string(event.Stage)),
					valueOrDash(outcome),
					oneLine(event.Message),
				); err != nil {
					return err
				}
			}
		}
	}
	return table.Flush()
}

func writeIndentedJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func formatHistoryTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func printHistoryUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  pgdrill history <command> [flags]

Commands:
  list             List recent execution attempts.
  show             Inspect one logical run and its attempts.
  import           Import an existing terminal JSON report.
  help             Show this help.

Store resolution:
  -store, PGDRILL_HISTORY_DIR, XDG_STATE_HOME, ~/.local/state/pgdrill/history

`)
}
