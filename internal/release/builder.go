package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/r314tive/pgdrill/internal/compatibility"
	"github.com/r314tive/pgdrill/internal/doccheck"
	"github.com/r314tive/pgdrill/internal/planner"
)

const (
	versionPackage             = "github.com/r314tive/pgdrill/internal/version"
	maxReleaseSupportFiles     = 4096
	maxReleaseSupportFileBytes = 16 << 20
	maxReleaseSupportBytes     = 128 << 20
)

type Target struct {
	OS   string
	Arch string
}

func (t Target) String() string {
	return t.OS + "/" + t.Arch
}

func DefaultTargets() []Target {
	return []Target{
		{OS: "linux", Arch: "amd64"},
		{OS: "linux", Arch: "arm64"},
		{OS: "darwin", Arch: "amd64"},
		{OS: "darwin", Arch: "arm64"},
	}
}

func ParseTargets(value string) ([]Target, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("release targets are required")
	}

	parts := strings.Split(value, ",")
	targets := make([]Target, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		goos, goarch, ok := strings.Cut(part, "/")
		if !ok || goos == "" || goarch == "" || strings.Contains(goarch, "/") {
			return nil, fmt.Errorf("invalid release target %q; expected goos/goarch", part)
		}
		target := Target{OS: goos, Arch: goarch}
		if err := validateTarget(target); err != nil {
			return nil, err
		}
		if _, ok := seen[target.String()]; ok {
			return nil, fmt.Errorf("duplicate release target %q", target)
		}
		seen[target.String()] = struct{}{}
		targets = append(targets, target)
	}
	return targets, nil
}

type Options struct {
	Version   string
	Commit    string
	Date      string
	SourceDir string
	OutputDir string
	GoBinary  string
	Targets   []Target
}

type Artifact struct {
	Name   string
	Path   string
	SHA256 string
}

type Result struct {
	Artifacts     []Artifact
	ChecksumsPath string
}

func Build(ctx context.Context, opts Options) (Result, error) {
	validated, releaseTime, err := validateOptions(opts)
	if err != nil {
		return Result{}, err
	}
	opts = validated
	supportEntries, err := loadReleaseSupportFiles(ctx, opts.SourceDir)
	if err != nil {
		return Result{}, err
	}
	goVersion, err := releaseSupportBody(supportEntries, ".go-version")
	if err != nil {
		return Result{}, err
	}
	expectedGoVersion := "go" + strings.TrimSpace(string(goVersion))
	if expectedGoVersion == "go" {
		return Result{}, fmt.Errorf(".go-version is empty")
	}
	activeGoVersion, err := goVersionForBuild(ctx, opts)
	if err != nil {
		return Result{}, err
	}
	if activeGoVersion != expectedGoVersion {
		return Result{}, fmt.Errorf("release toolchain mismatch: expected %s, got %s", expectedGoVersion, activeGoVersion)
	}

	supportDir, err := os.MkdirTemp("", "pgdrill-release-support-*")
	if err != nil {
		return Result{}, fmt.Errorf("create release support validation directory: %w", err)
	}
	defer os.RemoveAll(supportDir)
	if err := materializeReleaseSupportFiles(supportDir, supportEntries); err != nil {
		return Result{}, err
	}
	if issues, err := doccheck.CheckRepository(supportDir); err != nil {
		return Result{}, fmt.Errorf("validate packaged documentation: %w", err)
	} else if len(issues) != 0 {
		issue := issues[0]
		return Result{}, fmt.Errorf(
			"validate packaged documentation: %s:%d: invalid link %q: %s",
			issue.Source,
			issue.Line,
			issue.Destination,
			issue.Reason,
		)
	}

	compatibilityMatrix, err := releaseSupportBody(supportEntries, "compatibility/matrix.yaml")
	if err != nil {
		return Result{}, err
	}
	matrix, err := compatibility.Parse(compatibilityMatrix)
	if err != nil {
		return Result{}, fmt.Errorf("validate compatibility matrix: %w", err)
	}
	if err := matrix.ValidateReferences(supportDir); err != nil {
		return Result{}, fmt.Errorf("validate compatibility evidence references: %w", err)
	}
	fleetExample, err := releaseSupportBody(supportEntries, "examples/fleet.yaml")
	if err != nil {
		return Result{}, err
	}
	fleet, err := planner.Load(bytes.NewReader(fleetExample), "yaml")
	if err != nil {
		return Result{}, fmt.Errorf("validate fleet example: %w", err)
	}
	plan, err := planner.Build(fleet)
	if err != nil {
		return Result{}, fmt.Errorf("compile fleet example: %w", err)
	}
	if len(plan.Rejections) != 0 {
		return Result{}, fmt.Errorf("fleet example has %d placement rejections", len(plan.Rejections))
	}
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create release output directory: %w", err)
	}

	workDir, err := os.MkdirTemp(opts.OutputDir, ".pgdrill-release-*")
	if err != nil {
		return Result{}, fmt.Errorf("create release work directory: %w", err)
	}
	defer os.RemoveAll(workDir)

	versionName := strings.TrimPrefix(opts.Version, "v")
	artifacts := make([]Artifact, 0, len(opts.Targets))
	for _, target := range opts.Targets {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}

		rootName := fmt.Sprintf("pgdrill_%s_%s_%s", versionName, target.OS, target.Arch)
		binaryPath := filepath.Join(workDir, target.OS+"_"+target.Arch, "pgdrill")
		if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
			return Result{}, fmt.Errorf("create build directory for %s: %w", target, err)
		}
		if err := buildBinary(ctx, opts, target, binaryPath, releaseTime); err != nil {
			return Result{}, err
		}
		binary, err := os.ReadFile(binaryPath)
		if err != nil {
			return Result{}, fmt.Errorf("read %s binary: %w", target, err)
		}

		archiveName := rootName + ".tar.gz"
		archivePath := filepath.Join(workDir, archiveName)
		entries := make([]archiveEntry, 0, len(supportEntries)+1)
		for _, supportEntry := range supportEntries {
			entries = append(entries, archiveEntry{
				Name: filepath.ToSlash(filepath.Join(rootName, supportEntry.Name)),
				Mode: supportEntry.Mode,
				Body: supportEntry.Body,
			})
		}
		entries = append(entries, archiveEntry{
			Name: filepath.ToSlash(filepath.Join(rootName, "pgdrill")),
			Mode: 0o755,
			Body: binary,
		})
		if err := writeTarGz(archivePath, releaseTime, entries); err != nil {
			return Result{}, fmt.Errorf("package %s: %w", target, err)
		}
		digest, err := fileSHA256(archivePath)
		if err != nil {
			return Result{}, fmt.Errorf("checksum %s: %w", target, err)
		}
		artifacts = append(artifacts, Artifact{Name: archiveName, Path: archivePath, SHA256: digest})
	}

	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Name < artifacts[j].Name })
	checksumsName := fmt.Sprintf("pgdrill_%s_checksums.txt", versionName)
	checksumsPath := filepath.Join(workDir, checksumsName)
	if err := writeChecksums(checksumsPath, artifacts); err != nil {
		return Result{}, err
	}

	for i := range artifacts {
		finalPath := filepath.Join(opts.OutputDir, artifacts[i].Name)
		if err := replaceFile(artifacts[i].Path, finalPath); err != nil {
			return Result{}, fmt.Errorf("publish artifact %s: %w", artifacts[i].Name, err)
		}
		artifacts[i].Path = finalPath
	}
	finalChecksumsPath := filepath.Join(opts.OutputDir, checksumsName)
	if err := replaceFile(checksumsPath, finalChecksumsPath); err != nil {
		return Result{}, fmt.Errorf("publish checksums: %w", err)
	}

	return Result{Artifacts: artifacts, ChecksumsPath: finalChecksumsPath}, nil
}

func goVersionForBuild(ctx context.Context, opts Options) (string, error) {
	cmd := exec.CommandContext(ctx, opts.GoBinary, "env", "GOVERSION")
	cmd.Dir = opts.SourceDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("resolve release Go version: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func validateOptions(opts Options) (Options, time.Time, error) {
	if err := ValidateVersion(opts.Version); err != nil {
		return Options{}, time.Time{}, err
	}
	if !isHex(opts.Commit) || (len(opts.Commit) != 40 && len(opts.Commit) != 64) {
		return Options{}, time.Time{}, fmt.Errorf("release commit must be a full 40- or 64-character hexadecimal Git object ID")
	}
	releaseTime, err := time.Parse(time.RFC3339, opts.Date)
	if err != nil {
		return Options{}, time.Time{}, fmt.Errorf("release date must be RFC3339: %w", err)
	}
	if opts.SourceDir == "" {
		opts.SourceDir = "."
	}
	if opts.OutputDir == "" {
		opts.OutputDir = "dist"
	}
	sourceDir, err := filepath.Abs(opts.SourceDir)
	if err != nil {
		return Options{}, time.Time{}, fmt.Errorf("resolve release source directory: %w", err)
	}
	outputDir, err := filepath.Abs(opts.OutputDir)
	if err != nil {
		return Options{}, time.Time{}, fmt.Errorf("resolve release output directory: %w", err)
	}
	opts.SourceDir = sourceDir
	opts.OutputDir = outputDir
	if opts.GoBinary == "" {
		opts.GoBinary = "go"
	}
	if len(opts.Targets) == 0 {
		opts.Targets = DefaultTargets()
	}
	seen := make(map[string]struct{}, len(opts.Targets))
	for _, target := range opts.Targets {
		if err := validateTarget(target); err != nil {
			return Options{}, time.Time{}, err
		}
		if _, ok := seen[target.String()]; ok {
			return Options{}, time.Time{}, fmt.Errorf("duplicate release target %q", target)
		}
		seen[target.String()] = struct{}{}
	}
	return opts, releaseTime.UTC().Truncate(time.Second), nil
}

func validateTarget(target Target) error {
	switch target.OS {
	case "linux", "darwin":
	default:
		return fmt.Errorf("unsupported release operating system %q", target.OS)
	}
	switch target.Arch {
	case "amd64", "arm64":
	default:
		return fmt.Errorf("unsupported release architecture %q", target.Arch)
	}
	return nil
}

func buildBinary(ctx context.Context, opts Options, target Target, outputPath string, releaseTime time.Time) error {
	ldflags := releaseLDFlags(opts, releaseTime)
	cmd := exec.CommandContext(
		ctx,
		opts.GoBinary,
		"build",
		"-mod=readonly",
		"-trimpath",
		"-buildvcs=false",
		"-ldflags", ldflags,
		"-o", outputPath,
		"./cmd/pgdrill",
	)
	cmd.Dir = opts.SourceDir
	cmd.Env = buildEnvironment(os.Environ(), target)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build pgdrill for %s: %w: %s", target, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func releaseLDFlags(opts Options, releaseTime time.Time) string {
	return strings.Join([]string{
		"-s",
		"-w",
		"-buildid=",
		"-X", versionPackage + ".Version=" + opts.Version,
		"-X", versionPackage + ".Commit=" + opts.Commit,
		"-X", versionPackage + ".Date=" + releaseTime.Format(time.RFC3339),
	}, " ")
}

func buildEnvironment(current []string, target Target) []string {
	env := make([]string, 0, len(current)+8)
	for _, value := range current {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "GOOS", "GOARCH", "CGO_ENABLED", "GOFLAGS", "GOEXPERIMENT", "GOENV", "GOWORK", "GOAMD64", "GOARM64":
			continue
		}
		env = append(env, value)
	}
	env = append(env,
		"GOOS="+target.OS,
		"GOARCH="+target.Arch,
		"CGO_ENABLED=0",
		"GOFLAGS=",
		"GOEXPERIMENT=",
		"GOENV=off",
		"GOWORK=off",
	)
	if target.Arch == "amd64" {
		env = append(env, "GOAMD64=v1")
	} else {
		env = append(env, "GOARM64=v8.0")
	}
	return env
}

type archiveEntry struct {
	Name string
	Mode int64
	Body []byte
}

var releaseRootFiles = []string{
	".go-version",
	"CHANGELOG.md",
	"CONTRIBUTING.md",
	"LICENSE",
	"NOTICE",
	"README.md",
	"SECURITY.md",
}

var releaseEvidenceFiles = []string{
	"internal/adapters/barman/adapter_test.go",
	"internal/adapters/pgbackrest/adapter_test.go",
	"internal/adapters/pgprobackup/adapter_test.go",
	"internal/adapters/walg/adapter_test.go",
	"internal/targets/cnpg/verify_target_test.go",
	"internal/targets/local/target_test.go",
}

var releaseSupportDirectories = []string{
	"compatibility",
	"demo",
	"docs",
	"examples",
	"internal/adapters/barman/testdata",
	"internal/adapters/pgbackrest/testdata",
	"internal/adapters/pgprobackup/testdata",
	"internal/adapters/walg/testdata",
}

func loadReleaseSupportFiles(ctx context.Context, sourceDir string) ([]archiveEntry, error) {
	paths, err := trackedReleaseSupportPaths(ctx, sourceDir)
	if err != nil {
		return nil, err
	}
	entries := make([]archiveEntry, 0, len(paths))
	totalBytes := 0
	appendEntry := func(entry archiveEntry) error {
		if len(entries) == maxReleaseSupportFiles {
			return fmt.Errorf("release support files exceed maximum count %d", maxReleaseSupportFiles)
		}
		if len(entry.Body) > maxReleaseSupportBytes-totalBytes {
			return fmt.Errorf("release support files exceed %d total bytes", maxReleaseSupportBytes)
		}
		entries = append(entries, entry)
		totalBytes += len(entry.Body)
		return nil
	}
	for _, tracked := range paths {
		if shouldSkipReleaseSupportPath(tracked.Name) {
			continue
		}
		entry, err := readReleaseSupportFile(ctx, sourceDir, tracked)
		if err != nil {
			return nil, err
		}
		if err := appendEntry(entry); err != nil {
			return nil, err
		}
	}

	present := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		present[entry.Name] = struct{}{}
	}
	for _, name := range releaseRootFiles {
		if _, ok := present[name]; !ok {
			return nil, fmt.Errorf("required release support file %s is not tracked", name)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

type trackedReleaseSupportFile struct {
	Name     string
	ObjectID string
	Mode     int64
}

func trackedReleaseSupportPaths(ctx context.Context, sourceDir string) ([]trackedReleaseSupportFile, error) {
	args := []string{"ls-files", "--stage", "-z", "--"}
	args = append(args, releaseRootFiles...)
	args = append(args, releaseEvidenceFiles...)
	args = append(args, releaseSupportDirectories...)
	output, err := runGitBounded(ctx, sourceDir, 1<<20, args...)
	if err != nil {
		return nil, fmt.Errorf("list tracked release support files: %w", err)
	}

	paths := make([]trackedReleaseSupportFile, 0, len(releaseRootFiles))
	seen := make(map[string]struct{})
	for _, raw := range bytes.Split(output, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		metadata, pathBytes, ok := bytes.Cut(raw, []byte{'\t'})
		fields := strings.Fields(string(metadata))
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("invalid tracked release support index entry %q", raw)
		}
		mode, objectID, stage := fields[0], fields[1], fields[2]
		if stage != "0" {
			return nil, fmt.Errorf("tracked release support path %q has unresolved index stage %s", pathBytes, stage)
		}
		archiveMode := int64(0)
		switch mode {
		case "100644":
			archiveMode = 0o644
		case "100755":
			archiveMode = 0o755
		default:
			return nil, fmt.Errorf("tracked release support path %q has unsupported Git mode %s", pathBytes, mode)
		}
		if !isHex(objectID) || (len(objectID) != 40 && len(objectID) != 64) {
			return nil, fmt.Errorf("tracked release support path %q has invalid object id", pathBytes)
		}

		name := string(pathBytes)
		clean := filepath.Clean(filepath.FromSlash(name))
		if filepath.IsAbs(clean) || clean == "." || clean == ".." ||
			strings.HasPrefix(filepath.ToSlash(clean), "../") ||
			strings.IndexFunc(name, unicode.IsControl) >= 0 ||
			filepath.ToSlash(clean) != name {
			return nil, fmt.Errorf("invalid tracked release support path %q", name)
		}
		if !isReleaseSupportPath(name) {
			return nil, fmt.Errorf("tracked release support path %q is outside the release roots", name)
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("duplicate tracked release support path %q", name)
		}
		if len(paths) == maxReleaseSupportFiles {
			return nil, fmt.Errorf("release support files exceed maximum count %d", maxReleaseSupportFiles)
		}
		seen[name] = struct{}{}
		paths = append(paths, trackedReleaseSupportFile{Name: name, ObjectID: objectID, Mode: archiveMode})
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i].Name < paths[j].Name })
	return paths, nil
}

func isReleaseSupportPath(name string) bool {
	for _, root := range releaseRootFiles {
		if name == root {
			return true
		}
	}
	for _, evidence := range releaseEvidenceFiles {
		if name == evidence {
			return true
		}
	}
	for _, root := range releaseSupportDirectories {
		if strings.HasPrefix(name, root+"/") {
			return true
		}
	}
	return false
}

func readReleaseSupportFile(
	ctx context.Context,
	sourceDir string,
	tracked trackedReleaseSupportFile,
) (archiveEntry, error) {
	body, err := runGitBounded(ctx, sourceDir, maxReleaseSupportFileBytes, "cat-file", "blob", tracked.ObjectID)
	if err != nil {
		return archiveEntry{}, fmt.Errorf("read tracked release support file %s: %w", tracked.Name, err)
	}
	return archiveEntry{Name: tracked.Name, Mode: tracked.Mode, Body: body}, nil
}

func releaseSupportBody(entries []archiveEntry, name string) ([]byte, error) {
	for _, entry := range entries {
		if entry.Name == name {
			return entry.Body, nil
		}
	}
	return nil, fmt.Errorf("required release support file %s is not tracked", name)
}

func materializeReleaseSupportFiles(root string, entries []archiveEntry) error {
	for _, entry := range entries {
		path := filepath.Join(root, filepath.FromSlash(entry.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create packaged support directory for %s: %w", entry.Name, err)
		}
		if err := os.WriteFile(path, entry.Body, os.FileMode(entry.Mode)); err != nil {
			return fmt.Errorf("write packaged support file %s: %w", entry.Name, err)
		}
	}
	return nil
}

func runGitBounded(ctx context.Context, sourceDir string, limit int, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", sourceDir}, args...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, int64(limit)+1))
	if len(output) > limit {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("output exceeds %d bytes", limit)
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return nil, fmt.Errorf("%w: %s", waitErr, detail)
		}
		return nil, waitErr
	}
	return output, nil
}

func shouldSkipReleaseSupportPath(relativePath string) bool {
	parts := strings.Split(filepath.ToSlash(relativePath), "/")
	for _, name := range parts[:len(parts)-1] {
		switch name {
		case ".cache", ".git", ".notes", ".state", ".terraform", "bin", "dist":
			return true
		}
	}

	name := parts[len(parts)-1]
	lower := strings.ToLower(name)
	if lower == ".ds_store" || lower == ".gitignore" || lower == ".env" ||
		strings.HasPrefix(lower, ".env.") && lower != ".env.example" {
		return true
	}
	if strings.HasSuffix(lower, ".swp") || strings.HasSuffix(lower, ".swo") ||
		strings.HasSuffix(lower, "~") || strings.HasSuffix(lower, ".test") ||
		strings.HasSuffix(lower, ".coverprofile") || lower == "coverage.out" {
		return true
	}
	if strings.Contains(lower, ".tfstate") || strings.HasSuffix(lower, ".tfvars") ||
		strings.HasSuffix(lower, ".tfvars.json") {
		return true
	}
	if strings.HasSuffix(lower, ".plan") || strings.HasSuffix(lower, ".tfplan") {
		return true
	}
	return lower == "crash.log" || strings.HasPrefix(lower, "crash.") && strings.HasSuffix(lower, ".log")
}

func writeTarGz(path string, modTime time.Time, entries []archiveEntry) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	keep := false
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		if !keep {
			_ = os.Remove(path)
		}
	}()

	gzipWriter, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		return err
	}
	gzipWriter.Header.ModTime = modTime
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)

	sorted := append([]archiveEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, entry := range sorted {
		header := &tar.Header{
			Name:     entry.Name,
			Mode:     entry.Mode,
			Size:     int64(len(entry.Body)),
			ModTime:  modTime,
			Typeflag: tar.TypeReg,
			Format:   tar.FormatUSTAR,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			return err
		}
		if _, err := tarWriter.Write(entry.Body); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			return err
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	closed = true
	keep = true
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeChecksums(path string, artifacts []Artifact) error {
	var output strings.Builder
	for _, artifact := range artifacts {
		fmt.Fprintf(&output, "%s  %s\n", artifact.SHA256, artifact.Name)
	}
	if err := os.WriteFile(path, []byte(output.String()), 0o644); err != nil {
		return fmt.Errorf("write checksums: %w", err)
	}
	return nil
}

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}

func isHex(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}
