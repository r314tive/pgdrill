package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/r314tive/pgdrill/internal/planner"
)

func runPlan(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printPlanUsage(stderr)
		return 2
	}
	switch args[0] {
	case "validate":
		return runPlanValidate(args[1:], stdout, stderr)
	case "show":
		return runPlanShow(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printPlanUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown plan command %q\n\n", args[0])
		printPlanUsage(stderr)
		return 2
	}
}

func runPlanValidate(args []string, stdout, stderr io.Writer) int {
	fleetPath, ok, code := parsePlanFileFlags("plan validate", args, stderr)
	if !ok {
		return code
	}
	plan, err := loadPlan(fleetPath)
	if err != nil {
		fmt.Fprintf(stderr, "validate plan: %v\n", err)
		return 1
	}
	if err := writePlanSummary(stdout, plan, false); err != nil {
		fmt.Fprintf(stderr, "write plan validation: %v\n", err)
		return 1
	}
	if len(plan.Rejections) > 0 {
		fmt.Fprintf(stderr, "plan has %d rejected placement(s)\n", len(plan.Rejections))
		return 1
	}
	return 0
}

func runPlanShow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var fleetPath string
	var fleetPathLong string
	var format string
	fs.StringVar(&fleetPath, "f", "", "fleet file")
	fs.StringVar(&fleetPathLong, "file", "", "fleet file")
	fs.StringVar(&format, "format", "text", "output format: text or json")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "plan show does not accept positional arguments")
		return 2
	}
	if fleetPath == "" {
		fleetPath = fleetPathLong
	}
	if fleetPath == "" {
		fmt.Fprintln(stderr, "plan show requires -f or -file")
		return 2
	}
	plan, err := loadPlan(fleetPath)
	if err != nil {
		fmt.Fprintf(stderr, "show plan: %v\n", err)
		return 1
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "text":
		if err := writePlanSummary(stdout, plan, true); err != nil {
			fmt.Fprintf(stderr, "write plan: %v\n", err)
			return 1
		}
	case "json":
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(plan); err != nil {
			fmt.Fprintf(stderr, "write plan: %v\n", err)
			return 1
		}
	default:
		fmt.Fprintf(stderr, "unsupported format %q\n", format)
		return 2
	}
	return 0
}

func parsePlanFileFlags(name string, args []string, stderr io.Writer) (string, bool, int) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var fleetPath string
	var fleetPathLong string
	fs.StringVar(&fleetPath, "f", "", "fleet file")
	fs.StringVar(&fleetPathLong, "file", "", "fleet file")
	if ok, code := parseFlags(fs, args); !ok {
		return "", false, code
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "%s does not accept positional arguments\n", name)
		return "", false, 2
	}
	if fleetPath == "" {
		fleetPath = fleetPathLong
	}
	if fleetPath == "" {
		fmt.Fprintf(stderr, "%s requires -f or -file\n", name)
		return "", false, 2
	}
	return fleetPath, true, 0
}

func loadPlan(fleetPath string) (planner.Plan, error) {
	fleet, err := planner.LoadFile(fleetPath)
	if err != nil {
		return planner.Plan{}, err
	}
	return planner.Build(fleet)
}

func writePlanSummary(w io.Writer, plan planner.Plan, includeRuns bool) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"Schema", plan.SchemaVersion},
		{"Input digest", plan.InputDigest},
		{"Plan digest", plan.Digest},
		{"Max runs", strconv.Itoa(plan.MaxRuns)},
		{"Mutations", strconv.Itoa(plan.MutationCount)},
		{"Rejections", strconv.Itoa(len(plan.Rejections))},
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s:\t%s\n", row[0], row[1]); err != nil {
			return err
		}
	}
	if includeRuns && len(plan.Runs) > 0 {
		if _, err := fmt.Fprintln(table, "\nRUN\tRUN ID\tDRILL SET\tSOURCE\tTARGET\tPOLICY\tSPEC DIGEST"); err != nil {
			return err
		}
		for _, run := range plan.Runs {
			if _, err := fmt.Fprintf(
				table,
				"%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
				run.Ordinal,
				run.RunID,
				run.DrillSetRef.ID,
				run.SourceRef.ID,
				run.TargetRef.ID,
				run.PolicyRef.ID,
				run.SpecDigest,
			); err != nil {
				return err
			}
		}
	}
	if len(plan.Rejections) > 0 {
		if _, err := fmt.Fprintln(table, "\nDRILL SET\tSOURCE\tCODE\tMESSAGE"); err != nil {
			return err
		}
		for _, rejection := range plan.Rejections {
			if _, err := fmt.Fprintf(
				table,
				"%s\t%s\t%s\t%s\n",
				rejection.DrillSetID,
				valueOrDash(rejection.SourceID),
				rejection.Code,
				oneLine(rejection.Message),
			); err != nil {
				return err
			}
		}
	}
	return table.Flush()
}

func printPlanUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  pgdrill plan <command> [flags]

Commands:
  validate         Validate inventory, compatibility, bounds, and placement.
  show             Compile and print the immutable plan.
  help             Show this help.

`)
}
