package release

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	ociTestVersion = "v0.3.0-alpha.5"
	ociTestCommit  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ociTestDate    = "2026-07-28T00:00:00Z"
)

type ociFixtureOptions struct {
	corruptBlob    bool
	missingArm64   bool
	missingSBOM    bool
	wrongBinary    bool
	wrongImageUser bool
	whiteoutBinary bool
}

func TestVerifyOCIArchive(t *testing.T) {
	archive, dist := writeOCIFixture(t, ociFixtureOptions{})
	result, err := VerifyOCIArchive(OCIOptions{
		ArchivePath: archive,
		DistDir:     dist,
		Version:     ociTestVersion,
		Commit:      ociTestCommit,
		Date:        ociTestDate,
	})
	if err != nil {
		t.Fatalf("VerifyOCIArchive() error = %v", err)
	}
	wantPlatforms := []string{"linux/amd64", "linux/arm64"}
	if !equalStrings(result.Platforms, wantPlatforms) {
		t.Fatalf("platforms = %#v, want %#v", result.Platforms, wantPlatforms)
	}
	if !canonicalSHA256(result.IndexDigest) {
		t.Fatalf("index digest = %q", result.IndexDigest)
	}
	for _, platform := range wantPlatforms {
		if !canonicalSHA256(result.ManifestDigests[platform]) {
			t.Fatalf(
				"manifest digest for %s = %q",
				platform,
				result.ManifestDigests[platform],
			)
		}
	}
}

func TestVerifyOCIArchiveRejectsBrokenContracts(t *testing.T) {
	tests := []struct {
		name    string
		options ociFixtureOptions
		want    string
	}{
		{
			name:    "corrupt content addressed blob",
			options: ociFixtureOptions{corruptBlob: true},
			want:    "content digest",
		},
		{
			name:    "missing platform",
			options: ociFixtureOptions{missingArm64: true},
			want:    "platform count",
		},
		{
			name:    "missing sbom",
			options: ociFixtureOptions{missingSBOM: true},
			want:    "no SPDX SBOM",
		},
		{
			name:    "binary differs from archive",
			options: ociFixtureOptions{wrongBinary: true},
			want:    "does not match",
		},
		{
			name:    "root runtime user",
			options: ociFixtureOptions{wrongImageUser: true},
			want:    "image user",
		},
		{
			name:    "later layer removes binary",
			options: ociFixtureOptions{whiteoutBinary: true},
			want:    "has no /usr/local/bin/pgdrill",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive, dist := writeOCIFixture(t, test.options)
			_, err := VerifyOCIArchive(OCIOptions{
				ArchivePath: archive,
				DistDir:     dist,
				Version:     ociTestVersion,
				Commit:      ociTestCommit,
				Date:        ociTestDate,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyOCIArchive() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateOCIOptionsAcceptsFullGitObjectIDs(t *testing.T) {
	for _, commit := range []string{strings.Repeat("a", 40), strings.Repeat("b", 64)} {
		_, _, err := validateOCIOptions(OCIOptions{
			ArchivePath: "image.tar",
			Version:     ociTestVersion,
			Commit:      commit,
			Date:        ociTestDate,
		})
		if err != nil {
			t.Fatalf("validateOCIOptions(%d-character commit) error = %v", len(commit), err)
		}
	}
	_, _, err := validateOCIOptions(OCIOptions{
		ArchivePath: "image.tar",
		Version:     ociTestVersion,
		Commit:      "abc123",
		Date:        ociTestDate,
	})
	if err == nil || !strings.Contains(err.Error(), "40- or 64-character") {
		t.Fatalf("short commit error = %v", err)
	}
}

func writeOCIFixture(t *testing.T, options ociFixtureOptions) (string, string) {
	t.Helper()
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	releaseTime := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	architectures := []string{"amd64", "arm64"}
	if options.missingArm64 {
		architectures = architectures[:1]
	}

	blobs := map[string][]byte{}
	addBlob := func(data []byte, mediaType string) ociDescriptor {
		sum := sha256.Sum256(data)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		blobs["blobs/sha256/"+strings.TrimPrefix(digest, "sha256:")] = data
		return ociDescriptor{
			MediaType: mediaType,
			Digest:    digest,
			Size:      int64(len(data)),
		}
	}

	imageDescriptors := make([]ociDescriptor, 0, len(architectures))
	for _, architecture := range architectures {
		releaseBinary := []byte("pgdrill-" + architecture)
		writeReleaseFixture(t, dist, architecture, releaseBinary, releaseTime)

		imageBinary := releaseBinary
		if options.wrongBinary && architecture == "amd64" {
			imageBinary = []byte("tampered-amd64")
		}
		layerPath := filepath.Join(root, "layer-"+architecture+".tar.gz")
		if err := writeTarGz(layerPath, releaseTime, []archiveEntry{{
			Name: containerBinaryPath,
			Mode: 0o755,
			Body: imageBinary,
		}}); err != nil {
			t.Fatal(err)
		}
		layerData, err := os.ReadFile(layerPath)
		if err != nil {
			t.Fatal(err)
		}
		layer := addBlob(layerData, "application/vnd.oci.image.layer.v1.tar+gzip")
		layers := []ociDescriptor{layer}
		if options.whiteoutBinary && architecture == "amd64" {
			whiteoutPath := filepath.Join(root, "whiteout-"+architecture+".tar.gz")
			if err := writeTarGz(
				whiteoutPath,
				releaseTime,
				[]archiveEntry{{
					Name: "usr/local/bin/.wh.pgdrill",
					Mode: 0o000,
				}},
			); err != nil {
				t.Fatal(err)
			}
			whiteoutData, err := os.ReadFile(whiteoutPath)
			if err != nil {
				t.Fatal(err)
			}
			layers = append(
				layers,
				addBlob(whiteoutData, "application/vnd.oci.image.layer.v1.tar+gzip"),
			)
		}

		user := containerUser
		if options.wrongImageUser && architecture == "amd64" {
			user = "0:0"
		}
		configData := marshalJSON(t, map[string]any{
			"architecture": architecture,
			"os":           "linux",
			"config": map[string]any{
				"User":       user,
				"Env":        []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp"},
				"Entrypoint": []string{"/usr/local/bin/pgdrill"},
				"WorkingDir": containerWorkingDir,
				"StopSignal": "SIGTERM",
				"Labels": map[string]string{
					"org.opencontainers.image.title":       "pgdrill",
					"org.opencontainers.image.version":     ociTestVersion,
					"org.opencontainers.image.revision":    ociTestCommit,
					"org.opencontainers.image.created":     ociTestDate,
					"org.opencontainers.image.source":      "https://github.com/r314tive/pgdrill",
					"org.opencontainers.image.licenses":    "Apache-2.0",
					"org.opencontainers.image.base.name":   containerBaseName,
					"org.opencontainers.image.base.digest": containerBaseDigest,
				},
			},
		})
		config := addBlob(configData, ociConfigMediaType)
		manifestData := marshalJSON(t, ociManifest{
			SchemaVersion: 2,
			MediaType:     ociManifestMediaType,
			Config:        config,
			Layers:        layers,
		})
		manifest := addBlob(manifestData, ociManifestMediaType)
		manifest.Platform = &ociPlatform{OS: "linux", Architecture: architecture}
		imageDescriptors = append(imageDescriptors, manifest)
	}

	leaves := append([]ociDescriptor{}, imageDescriptors...)
	emptyConfig := addBlob([]byte("{}"), ociConfigMediaType)
	for _, image := range imageDescriptors {
		predicates := []string{provenancePredicateBase + "v1"}
		if !options.missingSBOM {
			predicates = append([]string{spdxPredicateType}, predicates...)
		}
		layers := make([]ociDescriptor, 0, len(predicates))
		for _, predicate := range predicates {
			statement := marshalJSON(t, map[string]any{
				"_type":         inTotoStatementType,
				"predicateType": predicate,
				"subject": []any{map[string]any{
					"name": "pgdrill",
					"digest": map[string]string{
						"sha256": strings.TrimPrefix(image.Digest, "sha256:"),
					},
				}},
				"predicate": map[string]any{},
			})
			layer := addBlob(statement, inTotoMediaType)
			layer.Annotations = map[string]string{"in-toto.io/predicate-type": predicate}
			layers = append(layers, layer)
		}
		attestationData := marshalJSON(t, ociManifest{
			SchemaVersion: 2,
			MediaType:     ociManifestMediaType,
			Config:        emptyConfig,
			Layers:        layers,
		})
		attestation := addBlob(attestationData, ociManifestMediaType)
		attestation.Platform = &ociPlatform{OS: "unknown", Architecture: "unknown"}
		attestation.Annotations = map[string]string{
			"vnd.docker.reference.type":   attestationReference,
			"vnd.docker.reference.digest": image.Digest,
		}
		leaves = append(leaves, attestation)
	}

	platformIndexData := marshalJSON(t, ociIndex{
		SchemaVersion: 2,
		MediaType:     ociIndexMediaType,
		Manifests:     leaves,
	})
	platformIndex := addBlob(platformIndexData, ociIndexMediaType)
	rootIndex := marshalJSON(t, ociIndex{
		SchemaVersion: 2,
		MediaType:     ociIndexMediaType,
		Manifests:     []ociDescriptor{platformIndex},
	})
	files := map[string][]byte{
		"index.json": rootIndex,
		"oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`),
	}
	for name, data := range blobs {
		files[name] = data
	}
	if options.corruptBlob {
		for name := range files {
			if strings.HasPrefix(name, "blobs/sha256/") {
				files[name] = append(files[name], 'x')
				break
			}
		}
	}

	archive := filepath.Join(root, "image.oci.tar")
	writeOCITar(t, archive, files)
	return archive, dist
}

func writeReleaseFixture(
	t *testing.T,
	dist,
	architecture string,
	binary []byte,
	releaseTime time.Time,
) {
	t.Helper()
	versionName := strings.TrimPrefix(ociTestVersion, "v")
	root := "pgdrill_" + versionName + "_linux_" + architecture
	if err := writeTarGz(
		filepath.Join(dist, root+".tar.gz"),
		releaseTime,
		[]archiveEntry{{Name: root + "/pgdrill", Mode: 0o755, Body: binary}},
	); err != nil {
		t.Fatal(err)
	}
}

func writeOCITar(t *testing.T, archive string, files map[string][]byte) {
	t.Helper()
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data := files[name]
		if err := writer.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o444,
			Size: int64(len(data)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func marshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
