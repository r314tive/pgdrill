package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowContract struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Needs       any               `yaml:"needs"`
	Permissions map[string]string `yaml:"permissions"`
	Env         map[string]string `yaml:"env"`
	Steps       []workflowStep    `yaml:"steps"`
}

type workflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Env  map[string]string `yaml:"env"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

func TestReleasePublishStepHasExplicitRepository(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	publish, ok := workflow.Jobs["publish"]
	if !ok {
		t.Fatal("release workflow has no publish job")
	}
	for _, step := range publish.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			t.Fatal("publish job must not check out unverified repository content")
		}
		if step.Name == "Publish release" {
			if got := step.Env["GH_REPO"]; got != "${{ github.repository }}" {
				t.Fatalf("publish step GH_REPO = %q, want github.repository", got)
			}
			return
		}
	}
	t.Fatal("release workflow has no Publish release step")
}

func TestReleaseBuildPassesFullCommitInput(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	build, ok := workflow.Jobs["build"]
	if !ok {
		t.Fatal("release workflow has no build job")
	}
	for _, step := range build.Steps {
		if step.Name != "Run release-grade checks" {
			continue
		}
		if !strings.Contains(step.Run, `RELEASE_COMMIT="$PGDRILL_COMMIT"`) {
			t.Fatalf("release-grade check does not pass the full commit input: %q", step.Run)
		}
		return
	}
	t.Fatal("release workflow has no Run release-grade checks step")
}

func TestReleaseSupplyChainJobsUseLeastPrivilege(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	assertPermissions(t, workflow.Jobs["build"], map[string]string{
		"contents":     "read",
		"id-token":     "write",
		"attestations": "write",
	})
	assertPermissions(t, workflow.Jobs["image"], map[string]string{
		"contents":     "read",
		"packages":     "write",
		"id-token":     "write",
		"attestations": "write",
	})
	assertPermissions(t, workflow.Jobs["publish"], map[string]string{
		"contents": "write",
	})
}

func TestReleaseSupplyChainActionsArePinned(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	expected := map[string]string{
		"Set up OCI verification Buildx": "docker/setup-buildx-action@" +
			"8d2750c68a42422c14e847fe6c8ac0403b4cbd6f",
		"Install pinned Syft": "anchore/sbom-action/download-syft@" +
			"e22c389904149dbc22b58101806040fa8d37a610",
		"Attest release SBOM": "actions/attest@" +
			"36051bcae73b7c2a8a6945a48cbf80953c6baa35",
		"Attest release bundle provenance": "actions/attest@" +
			"36051bcae73b7c2a8a6945a48cbf80953c6baa35",
		"Set up Docker Buildx": "docker/setup-buildx-action@" +
			"8d2750c68a42422c14e847fe6c8ac0403b4cbd6f",
		"Log in to GitHub Container Registry": "docker/login-action@" +
			"c94ce9fb468520275223c153574b00df6fe4bcc9",
		"Build and push multi-architecture image": "docker/build-push-action@" +
			"10e90e3645eae34f1e60eeb005ba3a3d33f178e8",
		"Attest image provenance": "actions/attest@" +
			"36051bcae73b7c2a8a6945a48cbf80953c6baa35",
	}
	found := make(map[string]string, len(expected))
	for _, jobName := range []string{"build", "image"} {
		for _, step := range workflow.Jobs[jobName].Steps {
			if _, ok := expected[step.Name]; ok {
				found[step.Name] = step.Uses
			}
		}
	}
	for name, want := range expected {
		if got := found[name]; got != want {
			t.Fatalf("release step %q uses %q, want %q", name, got, want)
		}
	}
}

func TestReleaseWorkflowBindsSBOMAndOCIProvenance(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	build := workflow.Jobs["build"]
	verifyOCI := findWorkflowStep(t, build, "Verify release OCI layout")
	for _, required := range []string{
		"make -s container-check",
		`VERSION="$GITHUB_REF_NAME"`,
		`RELEASE_COMMIT="$PGDRILL_COMMIT"`,
		`RELEASE_DATE="$PGDRILL_DATE"`,
		`RELEASE_EPOCH="${{ steps.release-metadata.outputs.epoch }}"`,
	} {
		if !strings.Contains(verifyOCI.Run, required) {
			t.Fatalf("release OCI verification does not contain %q", required)
		}
	}
	generateSBOM := findWorkflowStep(t, build, "Generate release SPDX SBOM")
	for _, required := range []string{
		"--source-name pgdrill-release",
		`--source-version "$GITHUB_REF_NAME"`,
		"--source-supplier r314tive",
		`.spdxVersion == "SPDX-2.3"`,
	} {
		if !strings.Contains(generateSBOM.Run, required) {
			t.Fatalf("release SBOM generation does not contain %q", required)
		}
	}
	sbom := findWorkflowStep(t, build, "Attest release SBOM")
	if !strings.Contains(sbom.With["subject-checksums"], "_checksums.txt") ||
		!strings.Contains(sbom.With["sbom-path"], "_sbom.spdx.json") {
		t.Fatalf("release SBOM attestation inputs = %#v", sbom.With)
	}
	provenance := findWorkflowStep(t, build, "Attest release bundle provenance")
	for _, pattern := range []string{"dist/*.tar.gz", "dist/*_checksums.txt", "dist/*_sbom.spdx.json"} {
		if !strings.Contains(provenance.With["subject-path"], pattern) {
			t.Fatalf("release provenance subjects %q do not contain %q", provenance.With["subject-path"], pattern)
		}
	}

	image := workflow.Jobs["image"]
	if !needsJobs(image.Needs, "build") {
		t.Fatalf("image job needs = %#v, want build", image.Needs)
	}
	buildImage := findWorkflowStep(t, image, "Build and push multi-architecture image")
	for key, want := range map[string]string{
		"context":    ".",
		"file":       "packaging/container/Dockerfile",
		"platforms":  "linux/amd64,linux/arm64",
		"push":       "true",
		"provenance": "mode=max",
	} {
		if got := buildImage.With[key]; got != want {
			t.Fatalf("image build input %s = %q, want %q", key, got, want)
		}
	}
	if buildImage.With["attests"] != "type=sbom,generator=${{ env.SBOM_GENERATOR }}" ||
		!strings.Contains(
			image.Env["SBOM_GENERATOR"],
			"docker/buildkit-syft-scanner@sha256:79e7b013",
		) {
		t.Fatalf(
			"image SBOM generator is not digest-pinned: input=%q env=%q",
			buildImage.With["attests"],
			image.Env["SBOM_GENERATOR"],
		)
	}
	imageAttestation := findWorkflowStep(t, image, "Attest image provenance")
	if imageAttestation.With["push-to-registry"] != "true" ||
		imageAttestation.With["create-storage-record"] != "false" {
		t.Fatalf("image attestation inputs = %#v", imageAttestation.With)
	}
	verifyImage := findWorkflowStep(t, image, "Retain and verify image identity")
	for _, required := range []string{
		`docker run --rm --platform linux/amd64`,
		`gh attestation verify "oci://$image@$IMAGE_DIGEST"`,
		`--source-digest "$COMMIT"`,
		`docker logout "$REGISTRY"`,
		`ln -s "$source_config/cli-plugins" "$anonymous_config/cli-plugins"`,
		`DOCKER_CONFIG="$anonymous_config"`,
		`docker buildx imagetools inspect "$image@$IMAGE_DIGEST"`,
	} {
		if !strings.Contains(verifyImage.Run, required) {
			t.Fatalf("image verification does not contain %q", required)
		}
	}

	publish := workflow.Jobs["publish"]
	if !needsJobs(publish.Needs, "build", "image") {
		t.Fatalf("publish job needs = %#v, want build and image", publish.Needs)
	}
	publishStep := findWorkflowStep(t, publish, "Publish release")
	for _, pattern := range []string{
		"*_sbom.spdx.json",
		"*.sigstore.json",
		"*_image.txt",
		"*_image-manifest.json",
	} {
		if !strings.Contains(publishStep.Run, pattern) {
			t.Fatalf("publish assets do not contain %q: %q", pattern, publishStep.Run)
		}
	}
}

func TestContainerPackagingIsMinimalAndPinned(t *testing.T) {
	root := filepath.Join("..", "..")
	dockerfile, err := os.ReadFile(filepath.Join(root, "packaging", "container", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(dockerfile)
	for _, required := range []string{
		"debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818",
		"ADD dist/pgdrill_${ARCHIVE_VERSION}_linux_${TARGETARCH}.tar.gz",
		"COPY --from=release --chmod=0755",
		"USER 65532:65532",
		`ENTRYPOINT ["/usr/local/bin/pgdrill"]`,
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("container Dockerfile does not contain %q", required)
		}
	}
	if strings.Contains(content, "\nRUN ") {
		t.Fatal("release Dockerfile must not install or execute an implicit provider toolchain")
	}

	ignore, err := os.ReadFile(filepath.Join(root, ".dockerignore"))
	if err != nil {
		t.Fatal(err)
	}
	ignoreContent := string(ignore)
	if !strings.HasPrefix(ignoreContent, "**\n") ||
		!strings.Contains(ignoreContent, "!dist/pgdrill_*_linux_*.tar.gz") {
		t.Fatalf("Docker build context is not archive-allowlisted:\n%s", ignoreContent)
	}
}

func assertPermissions(t *testing.T, job workflowJob, want map[string]string) {
	t.Helper()
	if len(job.Permissions) != len(want) {
		t.Fatalf("job permissions = %#v, want %#v", job.Permissions, want)
	}
	for name, permission := range want {
		if job.Permissions[name] != permission {
			t.Fatalf("permission %s = %q, want %q", name, job.Permissions[name], permission)
		}
	}
}

func findWorkflowStep(t *testing.T, job workflowJob, name string) workflowStep {
	t.Helper()
	for _, step := range job.Steps {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("release workflow has no %q step", name)
	return workflowStep{}
}

func needsJobs(value any, expected ...string) bool {
	var actual []string
	switch typed := value.(type) {
	case string:
		actual = []string{typed}
	case []any:
		for _, item := range typed {
			name, ok := item.(string)
			if !ok {
				return false
			}
			actual = append(actual, name)
		}
	default:
		return false
	}
	if len(actual) != len(expected) {
		return false
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}

func readReleaseWorkflow(t *testing.T) workflowContract {
	t.Helper()
	path := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}

	var workflow workflowContract
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse release workflow: %v", err)
	}
	return workflow
}
