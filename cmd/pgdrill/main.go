package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/r314tive/pgdrill/internal/model"
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
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "sample-config":
		return runSampleConfig(args[1:], stdout, stderr)
	case "explain":
		return runExplain(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
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
	if err := fs.Parse(args); err != nil {
		return 2
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
	if err := fs.Parse(args); err != nil {
		return 2
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
  version          Print the pgdrill version.
  sample-config    Print a starter configuration.
  explain          Explain the project model.
  help             Show this help.

`)
}

const sampleConfig = `cluster:
  name: production-main

provider:
  type: wal-g
  env:
    WALG_FILE_PREFIX: /backups/postgresql/production-main

target:
  type: local
  work_dir: /var/tmp/pgdrill/production-main

recovery:
  target: latest

probes:
  - type: pg_isready
  - type: sql
    name: server_version
    query: "select version()"
  - type: amcheck
    mode: heap

report:
  format: json
`

const explainText = `pgdrill model:

Provider       Discovers backup metadata and prepares provider-specific restore steps.
RestoreTarget  Provides disposable storage and runtime for a restore drill.
RecoveryTarget Describes what recovery point must be reached.
Probe          Runs post-restore checks against the recovered PostgreSQL instance.
EvidenceSink   Persists drill facts, timings, command outputs, and final status.

`
