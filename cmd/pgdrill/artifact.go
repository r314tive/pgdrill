package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/r314tive/pgdrill/internal/artifact"
	"github.com/r314tive/pgdrill/internal/history"
	"github.com/r314tive/pgdrill/internal/model"
)

func runArtifact(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printArtifactUsage(stderr)
		return 2
	}
	switch args[0] {
	case "verify":
		return runArtifactVerify(ctx, args[1:], stdout, stderr)
	case "gc":
		return runArtifactGC(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printArtifactUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown artifact command %q\n\n", args[0])
		printArtifactUsage(stderr)
		return 2
	}
}

func runArtifactVerify(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("artifact verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var storePath string
	var historyPath string
	var uriBase string
	var format string
	fs.StringVar(&storePath, "store", "", "artifact directory store path")
	fs.StringVar(&historyPath, "history-store", "", "complete canonical history reference scope")
	fs.StringVar(&uriBase, "uri-base", "", "expected relative artifact URI base")
	fs.StringVar(&format, "format", "text", "output format: text or json")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "artifact verify does not accept positional arguments")
		return 2
	}
	storePath, historyPath, format, ok := validateArtifactCommandInputs(
		storePath,
		historyPath,
		format,
		stderr,
	)
	if !ok {
		return 2
	}
	var verification artifact.VerificationResult
	err := withHistoryArtifactReferences(
		ctx,
		historyPath,
		func(references []model.ArtifactRef) error {
			var err error
			verification, err = (artifact.DirectoryStore{
				Path:    storePath,
				URIBase: strings.TrimSpace(uriBase),
			}).Verify(ctx, references)
			return err
		},
	)
	if err != nil {
		fmt.Fprintf(stderr, "verify artifact store: %v\n", err)
		return 1
	}
	if format == "json" {
		if err := writeIndentedJSON(stdout, verification); err != nil {
			fmt.Fprintf(stderr, "write artifact verification: %v\n", err)
			return 1
		}
		return 0
	}
	if err := writeArtifactVerificationText(
		stdout,
		storePath,
		historyPath,
		verification,
	); err != nil {
		fmt.Fprintf(stderr, "write artifact verification: %v\n", err)
		return 1
	}
	return 0
}

func runArtifactGC(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("artifact gc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var storePath string
	var historyPath string
	var uriBase string
	var beforeValue string
	var confirmation string
	var format string
	var includeAudit bool
	var includeLegacy bool
	var includeTemporary bool
	fs.StringVar(&storePath, "store", "", "artifact directory store path")
	fs.StringVar(&historyPath, "history-store", "", "complete canonical history reference scope")
	fs.StringVar(&uriBase, "uri-base", "", "expected relative artifact URI base")
	fs.StringVar(&beforeValue, "before", "", "remove eligible blobs last observed before this RFC3339 timestamp")
	fs.BoolVar(&includeAudit, "include-audit", false, "allow unreferenced audit-classified blobs")
	fs.BoolVar(&includeLegacy, "include-legacy", false, "allow unreferenced blobs without durable claims")
	fs.BoolVar(&includeTemporary, "include-temporary", false, "allow abandoned artifact temporary files")
	fs.StringVar(&confirmation, "confirm", "", "apply the exact sha256 plan digest")
	fs.StringVar(&format, "format", "text", "output format: text or json")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "artifact gc does not accept positional arguments")
		return 2
	}
	storePath, historyPath, format, ok := validateArtifactCommandInputs(
		storePath,
		historyPath,
		format,
		stderr,
	)
	if !ok {
		return 2
	}
	if strings.TrimSpace(beforeValue) == "" {
		fmt.Fprintln(stderr, "artifact gc requires -before with an RFC3339 timestamp")
		return 2
	}
	before, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(beforeValue))
	if err != nil {
		fmt.Fprintf(stderr, "artifact gc -before must be RFC3339: %v\n", err)
		return 2
	}
	policy := artifact.GCPolicy{
		Before:           before,
		IncludeAudit:     includeAudit,
		IncludeLegacy:    includeLegacy,
		IncludeTemporary: includeTemporary,
	}
	store := artifact.DirectoryStore{
		Path:    storePath,
		URIBase: strings.TrimSpace(uriBase),
	}
	confirmation = strings.TrimSpace(confirmation)
	if confirmation == "" {
		var plan artifact.GCPlan
		err := withHistoryArtifactReferences(
			ctx,
			historyPath,
			func(references []model.ArtifactRef) error {
				var err error
				plan, err = store.PlanGC(ctx, policy, references)
				return err
			},
		)
		if err != nil {
			fmt.Fprintf(stderr, "plan artifact GC: %v\n", err)
			return 1
		}
		if format == "json" {
			if err := writeIndentedJSON(stdout, plan); err != nil {
				fmt.Fprintf(stderr, "write artifact GC plan: %v\n", err)
				return 1
			}
			return 0
		}
		if err := writeArtifactGCPlanText(
			stdout,
			storePath,
			historyPath,
			plan,
		); err != nil {
			fmt.Fprintf(stderr, "write artifact GC plan: %v\n", err)
			return 1
		}
		return 0
	}

	var result artifact.GCResult
	err = withHistoryArtifactReferences(
		ctx,
		historyPath,
		func(references []model.ArtifactRef) error {
			var err error
			result, err = store.ApplyGC(ctx, policy, references, confirmation)
			return err
		},
	)
	if err != nil {
		fmt.Fprintf(stderr, "apply artifact GC: %v\n", err)
		return 1
	}
	if format == "json" {
		if err := writeIndentedJSON(stdout, result); err != nil {
			fmt.Fprintf(stderr, "write artifact GC result: %v\n", err)
			return 1
		}
		return 0
	}
	if err := writeArtifactGCResultText(stdout, storePath, historyPath, result); err != nil {
		fmt.Fprintf(stderr, "write artifact GC result: %v\n", err)
		return 1
	}
	return 0
}

func validateArtifactCommandInputs(
	storePath, historyPath, format string,
	stderr io.Writer,
) (string, string, string, bool) {
	if strings.TrimSpace(storePath) == "" {
		fmt.Fprintln(stderr, "artifact command requires an explicit -store")
		return "", "", "", false
	}
	if strings.TrimSpace(historyPath) == "" {
		fmt.Fprintln(stderr, "artifact command requires an explicit -history-store reference scope")
		return "", "", "", false
	}
	format, err := validateHistoryFormat(format)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return "", "", "", false
	}
	return filepath.Clean(storePath), filepath.Clean(historyPath), format, true
}

func withHistoryArtifactReferences(
	ctx context.Context,
	historyPath string,
	operation func([]model.ArtifactRef) error,
) error {
	return (history.DirectoryStore{Path: historyPath}).WithArtifactReferences(ctx, operation)
}

func writeArtifactVerificationText(
	w io.Writer,
	storePath, historyPath string,
	verification artifact.VerificationResult,
) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"Store", storePath},
		{"History scope", historyPath},
		{"Schema", verification.SchemaVersion},
		{"Store schema", valueOrDash(verification.StoreSchemaVersion)},
		{"Layout", strconv.Itoa(verification.LayoutVersion)},
		{"URI base", verification.URIBase},
		{"Blobs", strconv.Itoa(verification.Blobs)},
		{"Blob bytes", strconv.FormatInt(verification.BlobBytes, 10)},
		{"Managed blobs", strconv.Itoa(verification.ManagedBlobs)},
		{"Legacy blobs", strconv.Itoa(verification.LegacyBlobs)},
		{"Audit-classified blobs", strconv.Itoa(verification.AuditClassifiedBlobs)},
		{"Referenced blobs", strconv.Itoa(verification.ReferencedBlobs)},
		{"Unreferenced blobs", strconv.Itoa(verification.UnreferencedBlobs)},
		{"Reference occurrences", strconv.Itoa(verification.ReferenceOccurrences)},
		{"Foreign references", strconv.Itoa(verification.ForeignReferences)},
		{"Reference digest", verification.ReferenceDigest},
		{"Temporary files", strconv.Itoa(verification.TemporaryFiles)},
		{"Maintenance required", strconv.FormatBool(verification.MaintenanceRequired)},
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s:\t%s\n", row[0], oneLine(row[1])); err != nil {
			return err
		}
	}
	for _, digest := range verification.PendingGCOperations {
		if _, err := fmt.Fprintf(table, "Pending GC:\t%s\n", digest); err != nil {
			return err
		}
	}
	for _, digest := range verification.PendingGCCleanup {
		if _, err := fmt.Fprintf(table, "Pending cleanup:\t%s\n", digest); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writeArtifactGCPlanText(
	w io.Writer,
	storePath, historyPath string,
	plan artifact.GCPlan,
) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"Store", storePath},
		{"History scope", historyPath},
		{"Mode", "plan only; no artifact blobs deleted"},
		{"Schema", plan.SchemaVersion},
		{"Before", plan.Policy.Before.UTC().Format(time.RFC3339Nano)},
		{"Include audit", strconv.FormatBool(plan.Policy.IncludeAudit)},
		{"Include legacy", strconv.FormatBool(plan.Policy.IncludeLegacy)},
		{"Include temporary", strconv.FormatBool(plan.Policy.IncludeTemporary)},
		{"Reference digest", plan.ReferenceDigest},
		{"Plan digest", plan.Digest},
		{"Total blobs", strconv.Itoa(plan.Summary.TotalBlobs)},
		{"Referenced blobs", strconv.Itoa(plan.Summary.ReferencedBlobs)},
		{"Protected recent", strconv.Itoa(plan.Summary.ProtectedRecentBlobs)},
		{"Protected audit", strconv.Itoa(plan.Summary.ProtectedAuditBlobs)},
		{"Protected legacy", strconv.Itoa(plan.Summary.ProtectedLegacyBlobs)},
		{"Candidate blobs", strconv.Itoa(plan.Summary.CandidateBlobs)},
		{"Candidate blob bytes", strconv.FormatInt(plan.Summary.CandidateBlobBytes, 10)},
		{"Candidate temporary files", strconv.Itoa(plan.Summary.CandidateTemporaryFiles)},
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s:\t%s\n", row[0], oneLine(row[1])); err != nil {
			return err
		}
	}
	if len(plan.Blobs) > 0 {
		if _, err := fmt.Fprintln(
			table,
			"\nLAST OBSERVED\tBYTES\tLEGACY\tRETENTION\tARTIFACT ID",
		); err != nil {
			return err
		}
		for _, blob := range plan.Blobs {
			classes := make([]string, 0, len(blob.RetentionClasses))
			for _, class := range blob.RetentionClasses {
				classes = append(classes, string(class))
			}
			if _, err := fmt.Fprintf(
				table,
				"%s\t%d\t%t\t%s\t%s\n",
				blob.LastObservedAt.UTC().Format(time.RFC3339Nano),
				blob.SizeBytes,
				blob.Legacy,
				strings.Join(classes, ","),
				blob.ID,
			); err != nil {
				return err
			}
		}
	}
	return table.Flush()
}

func writeArtifactGCResultText(
	w io.Writer,
	storePath, historyPath string,
	result artifact.GCResult,
) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"Store", storePath},
		{"History scope", historyPath},
		{"Schema", result.SchemaVersion},
		{"Plan digest", result.PlanDigest},
		{"Deleted blobs", strconv.Itoa(result.DeletedBlobs)},
		{"Deleted blob bytes", strconv.FormatInt(result.DeletedBlobBytes, 10)},
		{"Deleted temporary files", strconv.Itoa(result.DeletedTemporaryFiles)},
		{"Deleted temporary bytes", strconv.FormatInt(result.DeletedTemporaryBytes, 10)},
		{"Resumed", strconv.FormatBool(result.Resumed)},
		{"Already applied", strconv.FormatBool(result.AlreadyApplied)},
		{"Reference scope changed", strconv.FormatBool(result.ReferenceScopeChanged)},
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s:\t%s\n", row[0], oneLine(row[1])); err != nil {
			return err
		}
	}
	return table.Flush()
}

func printArtifactUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  pgdrill artifact <command> [flags]

Commands:
  verify           Hash every local blob and resolve retained history references.
  gc               Plan or confirm age-gated reference-aware garbage collection.
  help             Show this help.

Safety boundary:
  -store and -history-store are explicit. The history store is held under a
  shared lock and must be the complete reference scope for this artifact store.

`)
}
