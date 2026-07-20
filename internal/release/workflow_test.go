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
	Steps []workflowStep `yaml:"steps"`
}

type workflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Env  map[string]string `yaml:"env"`
}

func TestReleasePublishStepHasExplicitRepository(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}

	var workflow workflowContract
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse release workflow: %v", err)
	}

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
