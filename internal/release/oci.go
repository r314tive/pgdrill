package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	ociIndexMediaType       = "application/vnd.oci.image.index.v1+json"
	ociManifestMediaType    = "application/vnd.oci.image.manifest.v1+json"
	ociConfigMediaType      = "application/vnd.oci.image.config.v1+json"
	inTotoMediaType         = "application/vnd.in-toto+json"
	attestationReference    = "attestation-manifest"
	spdxPredicateType       = "https://spdx.dev/Document"
	provenancePredicateBase = "https://slsa.dev/provenance/"
	inTotoStatementType     = "https://in-toto.io/Statement/v0.1"
	containerBinaryPath     = "usr/local/bin/pgdrill"
	containerUser           = "65532:65532"
	containerWorkingDir     = "/tmp"
	containerBaseName       = "docker.io/library/debian:bookworm-slim"
	containerBaseDigest     = "sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818"
	maxOCIArchiveBytes      = 512 << 20
	maxOCIFileBytes         = 128 << 20
	maxOCIArchiveEntries    = 10_000
)

type OCIOptions struct {
	ArchivePath string
	DistDir     string
	Version     string
	Commit      string
	Date        string
	Platforms   []Target
}

type OCIResult struct {
	IndexDigest     string
	Platforms       []string
	ManifestDigests map[string]string
}

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Platform    *ociPlatform      `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type ociPlatform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
}

type ociIndex struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Manifests     []ociDescriptor `json:"manifests"`
}

type ociManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Config        ociDescriptor   `json:"config"`
	Layers        []ociDescriptor `json:"layers"`
}

type ociImageConfig struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Config       struct {
		User       string            `json:"User"`
		Env        []string          `json:"Env"`
		Entrypoint []string          `json:"Entrypoint"`
		WorkingDir string            `json:"WorkingDir"`
		StopSignal string            `json:"StopSignal"`
		Labels     map[string]string `json:"Labels"`
	} `json:"config"`
}

type inTotoStatement struct {
	Type          string `json:"_type"`
	PredicateType string `json:"predicateType"`
	Subject       []struct {
		Digest map[string]string `json:"digest"`
	} `json:"subject"`
}

func VerifyOCIArchive(opts OCIOptions) (OCIResult, error) {
	validated, expectedDate, err := validateOCIOptions(opts)
	if err != nil {
		return OCIResult{}, err
	}
	opts = validated

	files, err := readOCIArchive(opts.ArchivePath)
	if err != nil {
		return OCIResult{}, err
	}
	if err := validateOCILayout(files); err != nil {
		return OCIResult{}, err
	}

	var root ociIndex
	if err := decodeOCIJSON(files["index.json"], "OCI root index", &root); err != nil {
		return OCIResult{}, err
	}
	leaves, indexDigest, err := collectOCILeaves(files, root, 0, map[string]bool{})
	if err != nil {
		return OCIResult{}, err
	}

	images := make(map[string]ociDescriptor, len(opts.Platforms))
	attestations := make(map[string]ociDescriptor, len(opts.Platforms))
	for _, descriptor := range leaves {
		switch {
		case descriptor.Platform != nil &&
			descriptor.Platform.OS == "linux" &&
			(descriptor.Platform.Architecture == "amd64" ||
				descriptor.Platform.Architecture == "arm64"):
			key := descriptor.Platform.OS + "/" + descriptor.Platform.Architecture
			if _, exists := images[key]; exists {
				return OCIResult{}, fmt.Errorf("OCI index contains duplicate platform %s", key)
			}
			images[key] = descriptor
		case descriptor.Platform != nil &&
			descriptor.Platform.OS == "unknown" &&
			descriptor.Platform.Architecture == "unknown" &&
			descriptor.Annotations["vnd.docker.reference.type"] == attestationReference:
			subject := descriptor.Annotations["vnd.docker.reference.digest"]
			if !canonicalSHA256(subject) {
				return OCIResult{}, fmt.Errorf("OCI attestation has invalid subject digest %q", subject)
			}
			if _, exists := attestations[subject]; exists {
				return OCIResult{}, fmt.Errorf("OCI index contains duplicate attestation for %s", subject)
			}
			attestations[subject] = descriptor
		default:
			return OCIResult{}, fmt.Errorf(
				"OCI index contains unsupported leaf descriptor %s for platform %s",
				descriptor.Digest,
				platformName(descriptor.Platform),
			)
		}
	}

	expectedPlatforms := make(map[string]struct{}, len(opts.Platforms))
	for _, platform := range opts.Platforms {
		expectedPlatforms[platform.String()] = struct{}{}
	}
	if len(images) != len(expectedPlatforms) {
		return OCIResult{}, fmt.Errorf(
			"OCI image platform count = %d, want %d",
			len(images),
			len(expectedPlatforms),
		)
	}

	platforms := make([]string, 0, len(expectedPlatforms))
	manifestDigests := make(map[string]string, len(expectedPlatforms))
	for platform := range expectedPlatforms {
		descriptor, ok := images[platform]
		if !ok {
			return OCIResult{}, fmt.Errorf("OCI image is missing platform %s", platform)
		}
		architecture := strings.TrimPrefix(platform, "linux/")
		if err := verifyOCIImage(
			files,
			descriptor,
			opts,
			expectedDate,
			architecture,
		); err != nil {
			return OCIResult{}, fmt.Errorf("verify OCI platform %s: %w", platform, err)
		}
		attestation, ok := attestations[descriptor.Digest]
		if !ok {
			return OCIResult{}, fmt.Errorf("OCI platform %s has no attestation manifest", platform)
		}
		if err := verifyOCIAttestation(files, attestation, descriptor.Digest); err != nil {
			return OCIResult{}, fmt.Errorf("verify OCI attestation for %s: %w", platform, err)
		}
		platforms = append(platforms, platform)
		manifestDigests[platform] = descriptor.Digest
	}
	if len(attestations) != len(expectedPlatforms) {
		return OCIResult{}, fmt.Errorf(
			"OCI attestation count = %d, want %d",
			len(attestations),
			len(expectedPlatforms),
		)
	}
	sort.Strings(platforms)
	return OCIResult{
		IndexDigest:     indexDigest,
		Platforms:       platforms,
		ManifestDigests: manifestDigests,
	}, nil
}

func validateOCIOptions(opts OCIOptions) (OCIOptions, time.Time, error) {
	if opts.ArchivePath == "" {
		return OCIOptions{}, time.Time{}, fmt.Errorf("OCI archive path is required")
	}
	if opts.DistDir == "" {
		opts.DistDir = filepath.Dir(opts.ArchivePath)
	}
	if err := ValidateVersion(opts.Version); err != nil {
		return OCIOptions{}, time.Time{}, err
	}
	if !isHex(opts.Commit) || (len(opts.Commit) != 40 && len(opts.Commit) != 64) {
		return OCIOptions{}, time.Time{}, fmt.Errorf(
			"OCI release commit must be a full 40- or 64-character hexadecimal Git object ID",
		)
	}
	releaseTime, err := time.Parse(time.RFC3339, opts.Date)
	if err != nil {
		return OCIOptions{}, time.Time{}, fmt.Errorf("OCI release date must be RFC3339: %w", err)
	}
	if len(opts.Platforms) == 0 {
		opts.Platforms = []Target{
			{OS: "linux", Arch: "amd64"},
			{OS: "linux", Arch: "arm64"},
		}
	}
	seen := make(map[string]struct{}, len(opts.Platforms))
	for _, platform := range opts.Platforms {
		if platform.OS != "linux" ||
			(platform.Arch != "amd64" && platform.Arch != "arm64") {
			return OCIOptions{}, time.Time{}, fmt.Errorf(
				"unsupported OCI platform %q",
				platform,
			)
		}
		if _, exists := seen[platform.String()]; exists {
			return OCIOptions{}, time.Time{}, fmt.Errorf(
				"duplicate OCI platform %q",
				platform,
			)
		}
		seen[platform.String()] = struct{}{}
	}
	return opts, releaseTime, nil
}

func readOCIArchive(archivePath string) (map[string][]byte, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open OCI archive: %w", err)
	}
	defer file.Close()

	files := make(map[string][]byte)
	reader := tar.NewReader(file)
	var total int64
	for entries := 0; ; entries++ {
		if entries >= maxOCIArchiveEntries {
			return nil, fmt.Errorf("OCI archive exceeds %d entries", maxOCIArchiveEntries)
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read OCI archive: %w", err)
		}
		rawName := header.Name
		if header.Typeflag == tar.TypeDir {
			rawName = strings.TrimSuffix(rawName, "/")
		}
		name := path.Clean(rawName)
		if name == "." || strings.HasPrefix(name, "/") ||
			name != rawName || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("OCI archive has unsafe path %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, tar.TypeRegA:
		default:
			return nil, fmt.Errorf("OCI archive entry %q is not a regular file", name)
		}
		if header.Size < 0 || header.Size > maxOCIFileBytes {
			return nil, fmt.Errorf("OCI archive entry %q has invalid size %d", name, header.Size)
		}
		total += header.Size
		if total > maxOCIArchiveBytes {
			return nil, fmt.Errorf("OCI archive exceeds %d bytes", maxOCIArchiveBytes)
		}
		if _, exists := files[name]; exists {
			return nil, fmt.Errorf("OCI archive has duplicate entry %q", name)
		}
		data, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
		if err != nil {
			return nil, fmt.Errorf("read OCI archive entry %q: %w", name, err)
		}
		if int64(len(data)) != header.Size {
			return nil, fmt.Errorf(
				"OCI archive entry %q size = %d, want %d",
				name,
				len(data),
				header.Size,
			)
		}
		files[name] = data
	}
	return files, nil
}

func validateOCILayout(files map[string][]byte) error {
	var layout struct {
		Version string `json:"imageLayoutVersion"`
	}
	if err := decodeOCIJSON(files["oci-layout"], "OCI layout", &layout); err != nil {
		return err
	}
	if layout.Version != "1.0.0" {
		return fmt.Errorf("OCI layout version = %q, want 1.0.0", layout.Version)
	}
	if len(files["index.json"]) == 0 {
		return fmt.Errorf("OCI archive has no index.json")
	}
	for name, data := range files {
		if !strings.HasPrefix(name, "blobs/sha256/") {
			continue
		}
		digest := strings.TrimPrefix(name, "blobs/sha256/")
		if len(digest) != 64 || !isHex(digest) || strings.ToLower(digest) != digest {
			return fmt.Errorf("OCI archive has invalid blob path %q", name)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != digest {
			return fmt.Errorf("OCI blob %s content digest = %s", digest, got)
		}
	}
	return nil
}

func collectOCILeaves(
	files map[string][]byte,
	index ociIndex,
	depth int,
	visiting map[string]bool,
) ([]ociDescriptor, string, error) {
	if depth > 4 {
		return nil, "", fmt.Errorf("OCI index nesting exceeds four levels")
	}
	if index.SchemaVersion != 2 || index.MediaType != ociIndexMediaType {
		return nil, "", fmt.Errorf(
			"OCI index schema/media type = %d/%q",
			index.SchemaVersion,
			index.MediaType,
		)
	}
	if len(index.Manifests) == 0 {
		return nil, "", fmt.Errorf("OCI index has no manifests")
	}

	var leaves []ociDescriptor
	indexDigest := ""
	for _, descriptor := range index.Manifests {
		data, err := descriptorBlob(files, descriptor)
		if err != nil {
			return nil, "", err
		}
		switch descriptor.MediaType {
		case ociIndexMediaType:
			if visiting[descriptor.Digest] {
				return nil, "", fmt.Errorf("OCI index cycle at %s", descriptor.Digest)
			}
			visiting[descriptor.Digest] = true
			var nested ociIndex
			if err := decodeOCIJSON(data, "OCI nested index", &nested); err != nil {
				return nil, "", err
			}
			nestedLeaves, nestedDigest, err := collectOCILeaves(
				files,
				nested,
				depth+1,
				visiting,
			)
			delete(visiting, descriptor.Digest)
			if err != nil {
				return nil, "", err
			}
			if indexDigest == "" {
				indexDigest = descriptor.Digest
			} else if nestedDigest != "" && nestedDigest != indexDigest {
				return nil, "", fmt.Errorf("OCI root resolves multiple image indexes")
			}
			leaves = append(leaves, nestedLeaves...)
		case ociManifestMediaType:
			leaves = append(leaves, descriptor)
		default:
			return nil, "", fmt.Errorf(
				"OCI index descriptor %s has unsupported media type %q",
				descriptor.Digest,
				descriptor.MediaType,
			)
		}
	}
	if indexDigest == "" {
		encoded, err := json.Marshal(index)
		if err != nil {
			return nil, "", fmt.Errorf("encode OCI index identity: %w", err)
		}
		sum := sha256.Sum256(encoded)
		indexDigest = "sha256:" + hex.EncodeToString(sum[:])
	}
	return leaves, indexDigest, nil
}

func verifyOCIImage(
	files map[string][]byte,
	descriptor ociDescriptor,
	opts OCIOptions,
	expectedDate time.Time,
	architecture string,
) error {
	manifestData, err := descriptorBlob(files, descriptor)
	if err != nil {
		return err
	}
	var manifest ociManifest
	if err := decodeOCIJSON(manifestData, "OCI image manifest", &manifest); err != nil {
		return err
	}
	if manifest.SchemaVersion != 2 || manifest.MediaType != ociManifestMediaType {
		return fmt.Errorf(
			"image manifest schema/media type = %d/%q",
			manifest.SchemaVersion,
			manifest.MediaType,
		)
	}
	if manifest.Config.MediaType != ociConfigMediaType {
		return fmt.Errorf("image config media type = %q", manifest.Config.MediaType)
	}
	configData, err := descriptorBlob(files, manifest.Config)
	if err != nil {
		return err
	}
	var config ociImageConfig
	if err := decodeOCIJSON(configData, "OCI image config", &config); err != nil {
		return err
	}
	if config.OS != "linux" || config.Architecture != architecture {
		return fmt.Errorf(
			"image config platform = %s/%s, want linux/%s",
			config.OS,
			config.Architecture,
			architecture,
		)
	}
	if config.Config.User != containerUser {
		return fmt.Errorf("image user = %q, want %q", config.Config.User, containerUser)
	}
	if config.Config.WorkingDir != containerWorkingDir {
		return fmt.Errorf(
			"image working directory = %q, want %q",
			config.Config.WorkingDir,
			containerWorkingDir,
		)
	}
	if !equalStrings(config.Config.Entrypoint, []string{"/usr/local/bin/pgdrill"}) {
		return fmt.Errorf("image entrypoint = %#v", config.Config.Entrypoint)
	}
	if config.Config.StopSignal != "SIGTERM" {
		return fmt.Errorf("image stop signal = %q, want SIGTERM", config.Config.StopSignal)
	}
	if countExactString(config.Config.Env, "HOME=/tmp") != 1 {
		return fmt.Errorf("image environment must contain HOME=/tmp exactly once")
	}
	expectedLabels := map[string]string{
		"org.opencontainers.image.title":       "pgdrill",
		"org.opencontainers.image.version":     opts.Version,
		"org.opencontainers.image.revision":    opts.Commit,
		"org.opencontainers.image.created":     expectedDate.Format(time.RFC3339),
		"org.opencontainers.image.source":      "https://github.com/r314tive/pgdrill",
		"org.opencontainers.image.licenses":    "Apache-2.0",
		"org.opencontainers.image.base.name":   containerBaseName,
		"org.opencontainers.image.base.digest": containerBaseDigest,
	}
	for key, want := range expectedLabels {
		if got := config.Config.Labels[key]; got != want {
			return fmt.Errorf("image label %s = %q, want %q", key, got, want)
		}
	}

	wantBinary, err := readReleaseBinary(opts.DistDir, opts.Version, architecture)
	if err != nil {
		return err
	}
	var imageBinary []byte
	var imageMode os.FileMode
	for _, layer := range manifest.Layers {
		layerData, err := descriptorBlob(files, layer)
		if err != nil {
			return err
		}
		candidate, mode, found, err := readBinaryFromLayer(layerData, layer.MediaType)
		if err != nil {
			return fmt.Errorf("read image layer %s: %w", layer.Digest, err)
		}
		if found {
			imageBinary = candidate
			imageMode = mode
		}
	}
	if imageBinary == nil {
		return fmt.Errorf("image has no /%s", containerBinaryPath)
	}
	if imageMode.Perm() != 0o755 {
		return fmt.Errorf("image pgdrill mode = %04o, want 0755", imageMode.Perm())
	}
	if !bytes.Equal(imageBinary, wantBinary) {
		return fmt.Errorf("image pgdrill does not match the %s release archive", architecture)
	}
	return nil
}

func verifyOCIAttestation(
	files map[string][]byte,
	descriptor ociDescriptor,
	subjectDigest string,
) error {
	data, err := descriptorBlob(files, descriptor)
	if err != nil {
		return err
	}
	var manifest ociManifest
	if err := decodeOCIJSON(data, "OCI attestation manifest", &manifest); err != nil {
		return err
	}
	if manifest.SchemaVersion != 2 || manifest.MediaType != ociManifestMediaType {
		return fmt.Errorf(
			"attestation manifest schema/media type = %d/%q",
			manifest.SchemaVersion,
			manifest.MediaType,
		)
	}
	if _, err := descriptorBlob(files, manifest.Config); err != nil {
		return fmt.Errorf("verify attestation config: %w", err)
	}
	seenSBOM := false
	seenProvenance := false
	for _, layer := range manifest.Layers {
		if layer.MediaType != inTotoMediaType {
			return fmt.Errorf("attestation layer %s has media type %q", layer.Digest, layer.MediaType)
		}
		predicate := layer.Annotations["in-toto.io/predicate-type"]
		statementData, err := descriptorBlob(files, layer)
		if err != nil {
			return err
		}
		var statement inTotoStatement
		if err := decodeOCIJSON(statementData, "in-toto statement", &statement); err != nil {
			return err
		}
		if statement.Type != inTotoStatementType {
			return fmt.Errorf("in-toto statement type = %q", statement.Type)
		}
		if statement.PredicateType != predicate {
			return fmt.Errorf(
				"attestation predicate annotation %q does not match statement %q",
				predicate,
				statement.PredicateType,
			)
		}
		if len(statement.Subject) != 1 ||
			statement.Subject[0].Digest["sha256"] != strings.TrimPrefix(subjectDigest, "sha256:") {
			return fmt.Errorf("attestation subject does not match %s", subjectDigest)
		}
		switch {
		case predicate == spdxPredicateType:
			seenSBOM = true
		case strings.HasPrefix(predicate, provenancePredicateBase):
			seenProvenance = true
		}
	}
	if !seenSBOM {
		return fmt.Errorf("attestation manifest has no SPDX SBOM predicate")
	}
	if !seenProvenance {
		return fmt.Errorf("attestation manifest has no SLSA provenance predicate")
	}
	return nil
}

func descriptorBlob(files map[string][]byte, descriptor ociDescriptor) ([]byte, error) {
	if !canonicalSHA256(descriptor.Digest) {
		return nil, fmt.Errorf("OCI descriptor has invalid digest %q", descriptor.Digest)
	}
	name := "blobs/sha256/" + strings.TrimPrefix(descriptor.Digest, "sha256:")
	data, ok := files[name]
	if !ok {
		return nil, fmt.Errorf("OCI descriptor blob %s is missing", descriptor.Digest)
	}
	if int64(len(data)) != descriptor.Size {
		return nil, fmt.Errorf(
			"OCI descriptor %s size = %d, want %d",
			descriptor.Digest,
			len(data),
			descriptor.Size,
		)
	}
	return data, nil
}

func readReleaseBinary(distDir, version, architecture string) ([]byte, error) {
	versionName := strings.TrimPrefix(version, "v")
	root := fmt.Sprintf("pgdrill_%s_linux_%s", versionName, architecture)
	archivePath := filepath.Join(distDir, root+".tar.gz")
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open release archive %s: %w", archivePath, err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("open release archive gzip %s: %w", archivePath, err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	expected := root + "/pgdrill"
	var binary []byte
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read release archive %s: %w", archivePath, err)
		}
		if header.Name != expected {
			continue
		}
		if binary != nil {
			return nil, fmt.Errorf("release archive %s has duplicate pgdrill", archivePath)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("release archive pgdrill is not a regular file")
		}
		if header.FileInfo().Mode().Perm() != 0o755 {
			return nil, fmt.Errorf("release archive pgdrill mode is not 0755")
		}
		binary, err = io.ReadAll(io.LimitReader(reader, maxOCIFileBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read release archive pgdrill: %w", err)
		}
		if len(binary) > maxOCIFileBytes {
			return nil, fmt.Errorf("release archive pgdrill exceeds %d bytes", maxOCIFileBytes)
		}
	}
	if binary == nil {
		return nil, fmt.Errorf("release archive %s has no pgdrill binary", archivePath)
	}
	return binary, nil
}

func readBinaryFromLayer(data []byte, mediaType string) ([]byte, os.FileMode, bool, error) {
	var source io.Reader = bytes.NewReader(data)
	var gzipReader *gzip.Reader
	switch mediaType {
	case "application/vnd.oci.image.layer.v1.tar+gzip":
		reader, err := gzip.NewReader(source)
		if err != nil {
			return nil, 0, false, err
		}
		gzipReader = reader
		source = reader
	case "application/vnd.oci.image.layer.v1.tar":
	default:
		return nil, 0, false, fmt.Errorf("unsupported OCI layer media type %q", mediaType)
	}
	if gzipReader != nil {
		defer gzipReader.Close()
	}
	reader := tar.NewReader(source)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil, 0, false, nil
		}
		if err != nil {
			return nil, 0, false, err
		}
		name := strings.TrimPrefix(path.Clean(header.Name), "./")
		if name != containerBinaryPath {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, 0, false, fmt.Errorf("pgdrill layer entry is not a regular file")
		}
		binary, err := io.ReadAll(io.LimitReader(reader, maxOCIFileBytes+1))
		if err != nil {
			return nil, 0, false, err
		}
		if len(binary) > maxOCIFileBytes {
			return nil, 0, false, fmt.Errorf("pgdrill layer entry exceeds %d bytes", maxOCIFileBytes)
		}
		return binary, header.FileInfo().Mode(), true, nil
	}
}

func decodeOCIJSON(data []byte, name string, target any) error {
	if len(data) == 0 {
		return fmt.Errorf("%s is missing or empty", name)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode %s: %w", name, err)
	}
	return nil
}

func canonicalSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	digest := strings.TrimPrefix(value, "sha256:")
	return isHex(digest) && digest == strings.ToLower(digest)
}

func platformName(platform *ociPlatform) string {
	if platform == nil {
		return "<none>"
	}
	name := platform.OS + "/" + platform.Architecture
	if platform.Variant != "" {
		name += "/" + platform.Variant
	}
	return name
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func countExactString(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}
