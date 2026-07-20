package cnpg

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/r314tive/pgdrill/internal/command"
)

const (
	DefaultPostgresContainer = "postgres"
	DefaultPodConnString     = "host=/controller/run dbname=postgres user=postgres"
)

// PodExecRunner executes logical probe commands inside the restored postgres
// container while retaining kubectl as the recorded compatibility transport.
type PodExecRunner struct {
	client    *KubectlClient
	spec      VerifyClusterSpec
	container string
}

func NewPodExecRunner(cfg KubectlConfig, spec VerifyClusterSpec, runner command.Runner) *PodExecRunner {
	return &PodExecRunner{
		client:    NewKubectlClient(cfg, runner),
		spec:      spec,
		container: DefaultPostgresContainer,
	}
}

func (r *PodExecRunner) Run(ctx context.Context, inv command.Invocation) (command.Result, error) {
	if r == nil || r.client == nil {
		return command.Result{}, fmt.Errorf("cnpg pod exec kubectl client is required")
	}
	if strings.TrimSpace(r.spec.InstancePodName) == "" {
		return command.Result{}, fmt.Errorf("cnpg pod exec instance pod is required")
	}
	if strings.TrimSpace(inv.Path) == "" {
		return command.Result{}, fmt.Errorf("cnpg pod exec command path is required")
	}
	if strings.TrimSpace(inv.WorkDir) != "" {
		return command.Result{}, fmt.Errorf("cnpg pod exec does not support command work_dir")
	}

	args := []string{"exec"}
	if inv.Stdin != nil {
		args = append(args, "-i")
	}
	args = append(args, r.spec.InstancePodName, "-c", r.container, "--")

	redactValues := append([]string{}, r.client.cfg.RedactValues...)
	redactValues = append(redactValues, inv.RedactValues...)
	if len(inv.Env) > 0 {
		args = append(args, "env")
		for _, name := range sortedEnvNames(inv.Env) {
			value := inv.Env[name]
			args = append(args, name+"="+value)
			redactValues = append(redactValues, value)
		}
	}
	args = append(args, inv.Path)
	args = append(args, inv.Args...)

	return r.client.runner.Run(ctx, command.Invocation{
		Path:             r.client.binary(),
		Args:             r.client.args(r.spec, args...),
		Timeout:          inv.Timeout,
		Stdin:            inv.Stdin,
		RedactValues:     redactValues,
		MaxOutputBytes:   inv.MaxOutputBytes,
		MaxEvidenceBytes: inv.MaxEvidenceBytes,
	})
}

func sortedEnvNames(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
