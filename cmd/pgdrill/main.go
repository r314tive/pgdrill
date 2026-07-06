package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/r314tive/pgdrill/internal/adapters"
	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/core"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/probes"
	"github.com/r314tive/pgdrill/internal/report"
	"github.com/r314tive/pgdrill/internal/targets"
	"github.com/r314tive/pgdrill/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	switch args[0] {
	case "run":
		return runDrill(args[1:], stdout, stderr)
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "sample-config":
		return runSampleConfig(args[1:], stdout, stderr)
	case "explain":
		return runExplain(args[1:], stdout, stderr)
	case "catalog":
		return runCatalog(args[1:], stdout, stderr)
	case "report":
		return runReport(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runDrill(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var configPath string
	var configPathLong string
	fs.StringVar(&configPath, "f", "", "configuration file")
	fs.StringVar(&configPathLong, "config", "", "configuration file")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "run does not accept positional arguments")
		return 2
	}
	if configPath == "" {
		configPath = configPathLong
	}
	if configPath == "" {
		fmt.Fprintln(stderr, "run requires -f or -config")
		return 2
	}

	cfg, err := config.LoadFile(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}
	if cfg.Report.Path == "" {
		fmt.Fprintln(stderr, "run requires report.path in config")
		return 2
	}

	provider, err := adapters.NewProvider(cfg.Provider, cfg.Restore)
	if err != nil {
		fmt.Fprintf(stderr, "create provider: %v\n", err)
		return 1
	}
	target, err := targets.NewRestoreTarget(cfg.Target)
	if err != nil {
		fmt.Fprintf(stderr, "create restore target: %v\n", err)
		return 1
	}
	configuredProbes, err := probes.NewProbes(cfg.Probes)
	if err != nil {
		fmt.Fprintf(stderr, "create probes: %v\n", err)
		return 1
	}

	result, runErr := core.Engine{
		Provider: provider,
		Target:   target,
		Probes:   configuredProbes,
		Sink:     report.JSONFileSink{Path: cfg.Report.Path},
	}.Run(context.Background(), core.DrillRequest{
		Target:         cfg.TargetSpec(),
		RecoveryTarget: cfg.RecoveryTarget(),
	})
	if err := writeRunSummary(stdout, result, cfg.Report.Path); err != nil {
		fmt.Fprintf(stderr, "write run summary: %v\n", err)
		return 1
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "run failed: %v\n", runErr)
		return 1
	}
	if result.Status != model.DrillStatusPassed {
		fmt.Fprintf(stderr, "run finished with status %s\n", result.Status)
		return 1
	}
	return 0
}

func runReport(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printReportUsage(stderr)
		return 2
	}

	switch args[0] {
	case "show":
		return runReportShow(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printReportUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown report command %q\n\n", args[0])
		printReportUsage(stderr)
		return 2
	}
}

func runReportShow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report show", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var format string
	fs.StringVar(&format, "format", "text", "output format: text or json")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "report show requires exactly one report path")
		return 2
	}

	result, err := report.ReadJSONFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "read report: %v\n", err)
		return 1
	}

	switch strings.ToLower(format) {
	case "text":
		if err := writeReportShowText(stdout, result); err != nil {
			fmt.Fprintf(stderr, "write report output: %v\n", err)
			return 1
		}
		return 0
	case "json":
		if err := report.WriteJSON(stdout, result); err != nil {
			fmt.Fprintf(stderr, "write report output: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "%v\n", errors.New("unsupported format: "+format))
		return 2
	}
}

func runCatalog(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printCatalogUsage(stderr)
		return 2
	}

	switch args[0] {
	case "list":
		return runCatalogList(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printCatalogUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown catalog command %q\n\n", args[0])
		printCatalogUsage(stderr)
		return 2
	}
}

func runCatalogList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("catalog list", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var configPath string
	var configPathLong string
	var format string
	var includeEvidence bool
	fs.StringVar(&configPath, "f", "", "configuration file")
	fs.StringVar(&configPathLong, "config", "", "configuration file")
	fs.StringVar(&format, "format", "text", "output format: text or json")
	fs.BoolVar(&includeEvidence, "evidence", false, "include command evidence in json output")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "catalog list does not accept positional arguments")
		return 2
	}
	if configPath == "" {
		configPath = configPathLong
	}
	if configPath == "" {
		fmt.Fprintln(stderr, "catalog list requires -f or -config")
		return 2
	}

	cfg, err := config.LoadFile(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}

	provider, err := adapters.NewProvider(cfg.Provider)
	if err != nil {
		fmt.Fprintf(stderr, "create provider: %v\n", err)
		return 1
	}

	catalog, err := provider.DiscoverBackups(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "discover backups: %v\n", err)
		return 1
	}

	output := catalogListOutput{
		Cluster:     cfg.Cluster.Name,
		Provider:    catalog.Provider,
		BackupCount: len(catalog.Backups),
		Backups:     catalog.Backups,
	}
	if includeEvidence {
		output.Evidence = catalog.Evidence
	}

	switch strings.ToLower(format) {
	case "text":
		if err := writeCatalogListText(stdout, output); err != nil {
			fmt.Fprintf(stderr, "write catalog list output: %v\n", err)
			return 1
		}
		return 0
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(output); err != nil {
			fmt.Fprintf(stderr, "write catalog list output: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "%v\n", errors.New("unsupported format: "+format))
		return 2
	}
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "version does not accept positional arguments")
		return 2
	}

	fmt.Fprintln(stdout, version.String())
	return 0
}

func runSampleConfig(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sample-config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "sample-config does not accept positional arguments")
		return 2
	}

	fmt.Fprint(stdout, sampleConfig)
	return 0
}

func runExplain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("explain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var format string
	fs.StringVar(&format, "format", "text", "output format: text or json")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "explain does not accept positional arguments")
		return 2
	}

	switch strings.ToLower(format) {
	case "text":
		fmt.Fprint(stdout, explainText)
		return 0
	case "json":
		doc := model.ProjectOverview()
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(doc); err != nil {
			fmt.Fprintf(stderr, "write explain output: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "%v\n", errors.New("unsupported format: "+format))
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `pgdrill verifies PostgreSQL recovery readiness.

Usage:
  pgdrill <command> [flags]

Commands:
  run              Execute a restore drill.
  version          Print the pgdrill version.
  sample-config    Print a starter configuration.
  explain          Explain the project model.
  catalog          Discover and inspect backup catalogs.
  report           Inspect drill reports.
  help             Show this help.

`)
}

func parseFlags(fs *flag.FlagSet, args []string) (bool, int) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, 0
		}
		return false, 2
	}
	return true, 0
}

func printReportUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  pgdrill report <command> [flags]

Commands:
  show             Print a drill report summary.
  help             Show this help.

`)
}

func printCatalogUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  pgdrill catalog <command> [flags]

Commands:
  list             Discover backups through the configured provider.
  help             Show this help.

`)
}

type catalogListOutput struct {
	Cluster     string                 `json:"cluster,omitempty"`
	Provider    model.ProviderType     `json:"provider"`
	BackupCount int                    `json:"backup_count"`
	Backups     []model.Backup         `json:"backups"`
	Evidence    []model.EvidenceRecord `json:"evidence,omitempty"`
}

func writeRunSummary(w io.Writer, result model.DrillResult, reportPath string) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"ID", valueOrDash(result.ID)},
		{"Status", string(result.Status)},
		{"Provider", string(result.Provider)},
		{"Backup", valueOrDash(result.Backup.ID)},
		{"Report", valueOrDash(reportPath)},
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s\t%s\n", row[0], row[1]); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writeCatalogListText(w io.Writer, output catalogListOutput) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "PROVIDER\tID\tKIND\tSTATUS\tSTARTED\tFINISHED"); err != nil {
		return err
	}
	for _, backup := range output.Backups {
		if _, err := fmt.Fprintf(
			table,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			backup.Provider,
			backup.ID,
			backup.Kind,
			backup.Status,
			timeField(backup.StartedAt),
			timeField(backup.FinishedAt),
		); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writeReportShowText(w io.Writer, result model.DrillResult) error {
	statusCounts := countCheckStatuses(result.Checks)
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	rows := [][2]string{
		{"ID", valueOrDash(result.ID)},
		{"Status", string(result.Status)},
		{"Provider", string(result.Provider)},
		{"Backup", valueOrDash(result.Backup.ID)},
		{"Target", targetSummary(result.Target)},
		{"Recovery", recoveryTargetSummary(result.RecoveryTarget)},
		{"Started", timeValue(result.StartedAt)},
		{"Finished", timeValue(result.FinishedAt)},
		{"Checks", fmt.Sprintf("%d passed, %d failed, %d warnings, %d skipped", statusCounts.passed, statusCounts.failed, statusCounts.warning, statusCounts.skipped)},
		{"Evidence", fmt.Sprintf("%d records", len(result.Evidence))},
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s\t%s\n", row[0], row[1]); err != nil {
			return err
		}
	}
	if len(result.Checks) > 0 {
		if _, err := fmt.Fprintln(table); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(table, "CHECK\tPROBE\tSTATUS\tMESSAGE"); err != nil {
			return err
		}
		for _, check := range result.Checks {
			if _, err := fmt.Fprintf(
				table,
				"%s\t%s\t%s\t%s\n",
				valueOrDash(check.Name),
				valueOrDash(string(check.Probe)),
				check.Status,
				oneLine(check.Message),
			); err != nil {
				return err
			}
		}
	}

	return table.Flush()
}

func timeField(value *time.Time) string {
	if value == nil || value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func timeValue(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func targetSummary(target model.TargetSpec) string {
	if target.Type == "" && target.WorkDir == "" {
		return "-"
	}
	if target.WorkDir == "" {
		return string(target.Type)
	}
	return string(target.Type) + " " + target.WorkDir
}

func recoveryTargetSummary(target model.RecoveryTarget) string {
	if target.Type == "" {
		return "-"
	}
	if target.Value == "" {
		return string(target.Type)
	}
	return string(target.Type) + " " + target.Value
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func oneLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

type checkStatusCounts struct {
	passed  int
	failed  int
	warning int
	skipped int
	unknown int
}

func countCheckStatuses(checks []model.Check) checkStatusCounts {
	var counts checkStatusCounts
	for _, check := range checks {
		switch check.Status {
		case model.CheckStatusPassed:
			counts.passed++
		case model.CheckStatusFailed:
			counts.failed++
		case model.CheckStatusWarning:
			counts.warning++
		case model.CheckStatusSkipped:
			counts.skipped++
		default:
			counts.unknown++
		}
	}
	return counts
}

const sampleConfig = `cluster:
  name: production-main

provider:
  type: wal-g
  timeout: 30s
  wal_verify:
    enabled: false
  env:
    WALG_FILE_PREFIX: /backups/postgresql/production-main

target:
  type: local
  work_dir: /var/tmp/pgdrill/production-main

restore:
  verify_backup:
    enabled: false

recovery:
  target: latest

probes:
  - type: pg_isready
    timeout: 10s
  - type: sql
    name: select_1
    query: "select 1"
    timeout: 10s
  - type: pg_dump
    mode: schema
    timeout: 30s

report:
  format: json
  path: ./pgdrill-report.json
`

const explainText = `pgdrill model:

Provider       Discovers backup metadata and prepares provider-specific restore steps.
RestoreTarget  Provides disposable storage and runtime for a restore drill.
RecoveryTarget Describes what recovery point must be reached.
Probe          Runs post-restore checks against the recovered PostgreSQL instance.
EvidenceSink   Persists drill facts, timings, command outputs, and final status.

`
