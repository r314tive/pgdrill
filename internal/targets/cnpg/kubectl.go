package cnpg

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

const defaultKubectlBinary = "kubectl"

type KubectlConfig struct {
	Binary       string
	Namespace    string
	Kubeconfig   string
	Context      string
	Timeout      time.Duration
	RedactValues []string
}

type KubectlClient struct {
	cfg    KubectlConfig
	runner command.Runner
}

func NewKubectlClient(cfg KubectlConfig, runner command.Runner) *KubectlClient {
	if runner == nil {
		runner = command.NewRunner(command.Options{DefaultTimeout: cfg.Timeout})
	}
	return &KubectlClient{
		cfg:    cfg,
		runner: runner,
	}
}

func (c *KubectlClient) ApplyCluster(ctx context.Context, spec VerifyClusterSpec, manifest []byte) ([]model.EvidenceRecord, error) {
	return c.strictRun(ctx, "kubectl-apply-cluster", c.args(spec, "apply", "-f", "-"), manifest, c.cfg.Timeout)
}

func (c *KubectlClient) WaitForInstanceReady(ctx context.Context, spec VerifyClusterSpec, opts WaitOptions) (Instance, []model.EvidenceRecord, error) {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultWaitTimeout
	}
	args := c.args(spec, "wait", "--for=condition=Ready", "pod/"+spec.InstancePodName, "--timeout="+durationSeconds(timeout))
	evidence, err := c.strictRun(ctx, "kubectl-wait-instance-ready", args, nil, timeout)
	if err != nil {
		return Instance{}, evidence, err
	}

	host := serviceHost(spec)
	return Instance{
		PodName:    spec.InstancePodName,
		Host:       host,
		Port:       DefaultPostgresPort,
		Database:   "postgres",
		ConnString: fmt.Sprintf("postgresql://%s:%d/postgres?sslmode=disable", host, DefaultPostgresPort),
	}, evidence, nil
}

func (c *KubectlClient) CaptureEvidence(ctx context.Context, spec VerifyClusterSpec, instance Instance, opts CaptureOptions) ([]model.EvidenceRecord, error) {
	commands := []struct {
		operation string
		args      []string
	}{
		{
			operation: "kubectl-capture-cluster-yaml",
			args:      c.args(spec, "get", "cluster.postgresql.cnpg.io", spec.Name, "-o", "yaml"),
		},
		{
			operation: "kubectl-capture-pods",
			args:      c.args(spec, "get", "pods", "-l", "cnpg.io/cluster="+spec.Name, "-o", "wide"),
		},
		{
			operation: "kubectl-capture-pvcs",
			args:      c.args(spec, "get", "pvc", "-l", "cnpg.io/cluster="+spec.Name, "-o", "wide"),
		},
		{
			operation: "kubectl-capture-events",
			args:      c.args(spec, "get", "events", "--sort-by=.metadata.creationTimestamp"),
		},
		{
			operation: "kubectl-capture-full-recovery-log",
			args:      c.args(spec, append([]string{"logs", "job/" + spec.FullRecoveryJob, "--timestamps"}, tailArgs(opts.PostgresLogTail)...)...),
		},
	}
	if instance.PodName != "" {
		commands = append(commands, struct {
			operation string
			args      []string
		}{
			operation: "kubectl-capture-postgres-log",
			args:      c.args(spec, append([]string{"logs", instance.PodName, "-c", "postgres", "--timestamps"}, tailArgs(opts.PostgresLogTail)...)...),
		})
	}

	var evidence []model.EvidenceRecord
	var joined error
	for _, cmd := range commands {
		commandEvidence, err := c.bestEffortRun(ctx, cmd.operation, cmd.args)
		evidence = append(evidence, commandEvidence...)
		joined = errors.Join(joined, err)
	}

	evidence = append(evidence, c.captureSummaryEvidence(spec, opts, joined))
	return evidence, nil
}

func (c *KubectlClient) DeleteCluster(ctx context.Context, spec VerifyClusterSpec) ([]model.EvidenceRecord, error) {
	return c.strictRun(ctx, "kubectl-delete-cluster", c.args(spec, "delete", "cluster.postgresql.cnpg.io", spec.Name, "--wait=true", "--timeout="+durationSeconds(c.deleteTimeout())), nil, c.deleteTimeout())
}

func (c *KubectlClient) DeletePVCs(ctx context.Context, spec VerifyClusterSpec) ([]model.EvidenceRecord, error) {
	return c.strictRun(ctx, "kubectl-delete-pvcs", c.args(spec, "delete", "pvc", "-l", "cnpg.io/cluster="+spec.Name, "--wait=true", "--timeout="+durationSeconds(c.deleteTimeout())), nil, c.deleteTimeout())
}

func (c *KubectlClient) strictRun(ctx context.Context, operation string, args []string, stdin []byte, timeout time.Duration) ([]model.EvidenceRecord, error) {
	evidence, result, err := c.run(ctx, operation, args, stdin, timeout)
	if err != nil {
		return evidence, err
	}
	if !result.Evidence.ExitStatus.Success {
		return evidence, fmt.Errorf("%s failed: %s", operation, result.Evidence.ExitStatus.Summary())
	}
	return evidence, nil
}

func (c *KubectlClient) bestEffortRun(ctx context.Context, operation string, args []string) ([]model.EvidenceRecord, error) {
	evidence, result, err := c.run(ctx, operation, args, nil, c.cfg.Timeout)
	if err != nil {
		return evidence, err
	}
	if !result.Evidence.ExitStatus.Success {
		return evidence, fmt.Errorf("%s failed: %s", operation, result.Evidence.ExitStatus.Summary())
	}
	return evidence, nil
}

func (c *KubectlClient) run(ctx context.Context, operation string, args []string, stdin []byte, timeout time.Duration) ([]model.EvidenceRecord, command.Result, error) {
	result, err := c.runner.Run(ctx, command.Invocation{
		Path:         c.binary(),
		Args:         args,
		Stdin:        stdin,
		Timeout:      timeout,
		RedactValues: c.cfg.RedactValues,
	})
	evidence := []model.EvidenceRecord{kubectlCommandEvidence(operation, result.Evidence)}
	if err != nil {
		return evidence, result, fmt.Errorf("%s: %w", operation, err)
	}
	return evidence, result, nil
}

func (c *KubectlClient) args(spec VerifyClusterSpec, args ...string) []string {
	result := []string{}
	if c.cfg.Kubeconfig != "" {
		result = append(result, "--kubeconfig", c.cfg.Kubeconfig)
	}
	if c.cfg.Context != "" {
		result = append(result, "--context", c.cfg.Context)
	}
	namespace := firstNonEmpty(c.cfg.Namespace, spec.Namespace)
	if namespace != "" {
		result = append(result, "-n", namespace)
	}
	return append(result, args...)
}

func (c *KubectlClient) binary() string {
	if strings.TrimSpace(c.cfg.Binary) != "" {
		return strings.TrimSpace(c.cfg.Binary)
	}
	return defaultKubectlBinary
}

func (c *KubectlClient) deleteTimeout() time.Duration {
	if c.cfg.Timeout != 0 {
		return c.cfg.Timeout
	}
	return DefaultWaitTimeout
}

func kubectlCommandEvidence(operation string, evidence model.CommandEvidence) model.EvidenceRecord {
	collectedAt := evidence.FinishedAt
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}
	return model.EvidenceRecord{
		ID:          "cnpg:" + operation + ":" + collectedAt.Format(time.RFC3339Nano),
		Kind:        model.EvidenceCommand,
		Source:      string(model.RestoreTargetKubernetes),
		CollectedAt: collectedAt,
		Command:     &evidence,
		Attributes: map[string]string{
			"operation": operation,
		},
	}
}

func (c *KubectlClient) captureSummaryEvidence(spec VerifyClusterSpec, opts CaptureOptions, captureErr error) model.EvidenceRecord {
	now := time.Now().UTC()
	attributes := map[string]string{
		"cluster":        spec.Name,
		"namespace":      firstNonEmpty(c.cfg.Namespace, spec.Namespace),
		"operation":      "kubectl-capture-summary",
		"reason":         opts.Reason,
		"postgres_tail":  strconv.Itoa(opts.PostgresLogTail),
		"events_tail":    strconv.Itoa(opts.EventsTail),
		"best_effort":    "true",
		"capture_status": "passed",
	}
	if captureErr != nil {
		attributes["capture_status"] = "warning"
		attributes["capture_error"] = captureErr.Error()
	}
	return model.EvidenceRecord{
		ID:          "cnpg:kubectl-capture-summary:" + now.Format(time.RFC3339Nano),
		Kind:        model.EvidenceRuntime,
		Source:      string(model.RestoreTargetKubernetes),
		CollectedAt: now,
		Attributes:  attributes,
	}
}

func serviceHost(spec VerifyClusterSpec) string {
	host := spec.Name + "-rw"
	if spec.Namespace != "" {
		host += "." + spec.Namespace + ".svc"
	}
	return host
}

func durationSeconds(duration time.Duration) string {
	if duration <= 0 {
		return "0s"
	}
	return strconv.Itoa(int(duration.Round(time.Second).Seconds())) + "s"
}

func tailArgs(tail int) []string {
	if tail <= 0 {
		return nil
	}
	return []string{"--tail=" + strconv.Itoa(tail)}
}
