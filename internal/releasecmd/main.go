package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	pgrelease "github.com/r314tive/pgdrill/internal/release"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stderr)
		return 2
	}
	switch args[0] {
	case "artifacts":
		return runArtifacts(args[1:], stdout, stderr)
	case "notes":
		return runNotes(args[1:], stdout, stderr)
	case "verify-oci":
		return runVerifyOCI(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown release command %q\n", args[0])
		writeUsage(stderr)
		return 2
	}
}

func runArtifacts(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("artifacts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var version, commit, date, sourceDir, outputDir, targetsValue string
	fs.StringVar(&version, "version", "", "release version tag")
	fs.StringVar(&commit, "commit", "", "release Git commit")
	fs.StringVar(&date, "date", "", "release date in RFC3339 format")
	fs.StringVar(&sourceDir, "source", ".", "repository source directory")
	fs.StringVar(&outputDir, "output", "dist", "artifact output directory")
	fs.StringVar(&targetsValue, "targets", defaultTargetsValue(), "comma-separated goos/goarch targets")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "artifacts does not accept positional arguments")
		return 2
	}
	targets, err := pgrelease.ParseTargets(targetsValue)
	if err != nil {
		fmt.Fprintf(stderr, "parse release targets: %v\n", err)
		return 2
	}
	result, err := pgrelease.Build(context.Background(), pgrelease.Options{
		Version:   version,
		Commit:    commit,
		Date:      date,
		SourceDir: sourceDir,
		OutputDir: outputDir,
		Targets:   targets,
	})
	if err != nil {
		fmt.Fprintf(stderr, "build release artifacts: %v\n", err)
		return 1
	}
	for _, artifact := range result.Artifacts {
		fmt.Fprintf(stdout, "%s  %s\n", artifact.SHA256, artifact.Path)
	}
	fmt.Fprintf(stdout, "checksums  %s\n", result.ChecksumsPath)
	return 0
}

func runNotes(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("notes", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var version, changelogPath, outputPath string
	fs.StringVar(&version, "version", "", "release version tag")
	fs.StringVar(&changelogPath, "changelog", "CHANGELOG.md", "changelog path")
	fs.StringVar(&outputPath, "output", "dist/RELEASE_NOTES.md", "release notes output path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "notes does not accept positional arguments")
		return 2
	}
	if err := pgrelease.WriteReleaseNotes(changelogPath, outputPath, version); err != nil {
		fmt.Fprintf(stderr, "write release notes: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, outputPath)
	return 0
}

func runVerifyOCI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify-oci", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var archivePath, distDir, version, commit, date string
	fs.StringVar(&archivePath, "image", "", "OCI layout archive path")
	fs.StringVar(&distDir, "dist", "dist", "release archive directory")
	fs.StringVar(&version, "version", "", "release version tag")
	fs.StringVar(&commit, "commit", "", "full release Git commit")
	fs.StringVar(&date, "date", "", "release date in RFC3339 format")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "verify-oci does not accept positional arguments")
		return 2
	}
	result, err := pgrelease.VerifyOCIArchive(pgrelease.OCIOptions{
		ArchivePath: archivePath,
		DistDir:     distDir,
		Version:     version,
		Commit:      commit,
		Date:        date,
	})
	if err != nil {
		fmt.Fprintf(stderr, "verify OCI image: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "index  %s\n", result.IndexDigest)
	for _, platform := range result.Platforms {
		fmt.Fprintf(
			stdout,
			"platform  %s  %s\n",
			platform,
			result.ManifestDigests[platform],
		)
	}
	return 0
}

func defaultTargetsValue() string {
	values := make([]string, 0, len(pgrelease.DefaultTargets()))
	for _, target := range pgrelease.DefaultTargets() {
		values = append(values, target.String())
	}
	return strings.Join(values, ",")
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: go run ./internal/releasecmd <artifacts|notes|verify-oci> [flags]")
}
