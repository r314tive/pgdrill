package release

import (
	"archive/tar"
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
)

const versionPackage = "github.com/r314tive/pgdrill/internal/version"

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
	goVersion, err := os.ReadFile(filepath.Join(opts.SourceDir, ".go-version"))
	if err != nil {
		return Result{}, fmt.Errorf("read .go-version: %w", err)
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

	readme, err := os.ReadFile(filepath.Join(opts.SourceDir, "README.md"))
	if err != nil {
		return Result{}, fmt.Errorf("read README.md: %w", err)
	}
	license, err := os.ReadFile(filepath.Join(opts.SourceDir, "LICENSE"))
	if err != nil {
		return Result{}, fmt.Errorf("read LICENSE: %w", err)
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
		entries := []archiveEntry{
			{Name: filepath.ToSlash(filepath.Join(rootName, ".go-version")), Mode: 0o644, Body: goVersion},
			{Name: filepath.ToSlash(filepath.Join(rootName, "LICENSE")), Mode: 0o644, Body: license},
			{Name: filepath.ToSlash(filepath.Join(rootName, "README.md")), Mode: 0o644, Body: readme},
			{Name: filepath.ToSlash(filepath.Join(rootName, "pgdrill")), Mode: 0o755, Body: binary},
		}
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
	if !isHex(opts.Commit) || len(opts.Commit) < 7 || len(opts.Commit) > 64 {
		return Options{}, time.Time{}, fmt.Errorf("release commit must be a 7-64 character hexadecimal Git object ID")
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
	ldflags := strings.Join([]string{
		"-s",
		"-w",
		"-X", versionPackage + ".Version=" + opts.Version,
		"-X", versionPackage + ".Commit=" + opts.Commit,
		"-X", versionPackage + ".Date=" + releaseTime.Format(time.RFC3339),
	}, " ")
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
