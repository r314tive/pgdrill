package cnpg

import (
	"context"
	"encoding/json"
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
	pollInterval := opts.PollInterval
	if pollInterval == 0 {
		pollInterval = DefaultPollInterval
	}

	deadline := time.Now().Add(timeout)
	evidence := []model.EvidenceRecord{}
	for {
		failed, reason, recoveryEvidence, err := c.fullRecoveryFailed(ctx, spec)
		evidence = append(evidence, recoveryEvidence...)
		if err != nil {
			return Instance{}, evidence, err
		}
		if failed {
			return Instance{}, evidence, fmt.Errorf("CNPG full-recovery failed before instance pod became Ready: %s", reason)
		}

		ready, podEvidence, err := c.instancePodReady(ctx, spec)
		evidence = append(evidence, podEvidence...)
		if err != nil {
			return Instance{}, evidence, err
		}
		if ready {
			host := serviceHost(spec)
			return Instance{
				PodName:    spec.InstancePodName,
				Host:       host,
				Port:       DefaultPostgresPort,
				Database:   "postgres",
				ConnString: fmt.Sprintf("postgresql://%s:%d/postgres?sslmode=disable", host, DefaultPostgresPort),
			}, evidence, nil
		}

		if time.Now().After(deadline) {
			return Instance{}, evidence, fmt.Errorf("timeout waiting for CNPG instance pod %q to become Ready", spec.InstancePodName)
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Instance{}, evidence, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *KubectlClient) fullRecoveryFailed(ctx context.Context, spec VerifyClusterSpec) (bool, string, []model.EvidenceRecord, error) {
	args := c.args(spec, "get", "pods", "-l", "cnpg.io/cluster="+spec.Name+",cnpg.io/jobRole=full-recovery", "-o", "json")
	evidence, result, err := c.run(ctx, "kubectl-check-full-recovery", args, nil, c.cfg.Timeout)
	if err != nil {
		return false, "", evidence, err
	}
	if !result.Evidence.ExitStatus.Success {
		return false, "", evidence, nil
	}
	failed, reason, err := fullRecoveryFailed(result.Raw.Stdout)
	if err != nil {
		return false, "", evidence, err
	}
	return failed, reason, evidence, nil
}

func (c *KubectlClient) instancePodReady(ctx context.Context, spec VerifyClusterSpec) (bool, []model.EvidenceRecord, error) {
	args := c.args(spec, "get", "pod", spec.InstancePodName, "-o", "json")
	evidence, result, err := c.run(ctx, "kubectl-check-instance-ready", args, nil, c.cfg.Timeout)
	if err != nil {
		return false, evidence, err
	}
	if !result.Evidence.ExitStatus.Success {
		return false, evidence, nil
	}
	ready, err := podReady(result.Raw.Stdout)
	if err != nil {
		return false, evidence, err
	}
	return ready, evidence, nil
}

func (c *KubectlClient) CaptureEvidence(ctx context.Context, spec VerifyClusterSpec, instance Instance, opts CaptureOptions) ([]model.EvidenceRecord, error) {
	type captureCommand struct {
		operation  string
		args       []string
		stdoutTail int
	}

	commands := []captureCommand{
		{
			operation: "kubectl-capture-cluster-yaml",
			args:      c.args(spec, "get", "cluster.postgresql.cnpg.io", spec.Name, "-o", "yaml"),
		},
		{
			operation: "kubectl-capture-pods",
			args:      c.args(spec, "get", "pods", "-l", "cnpg.io/cluster="+spec.Name, "-o", "wide"),
		},
		{
			operation: "kubectl-capture-instance-describe",
			args:      c.args(spec, "describe", "pod", spec.InstancePodName),
		},
		{
			operation: "kubectl-capture-pvcs",
			args:      c.args(spec, "get", "pvc", "-l", "cnpg.io/cluster="+spec.Name, "-o", "wide"),
		},
		{
			operation:  "kubectl-capture-events",
			args:       c.args(spec, "get", "events", "--sort-by=.metadata.creationTimestamp"),
			stdoutTail: opts.EventsTail,
		},
		{
			operation: "kubectl-capture-full-recovery-describe",
			args:      c.args(spec, "describe", "job/"+spec.FullRecoveryJob),
		},
		{
			operation: "kubectl-capture-full-recovery-log",
			args:      c.args(spec, append([]string{"logs", "job/" + spec.FullRecoveryJob, "--timestamps"}, tailArgs(opts.PostgresLogTail)...)...),
		},
		{
			operation: "kubectl-capture-full-recovery-bootstrap-log",
			args:      c.args(spec, append([]string{"logs", "job/" + spec.FullRecoveryJob, "-c", "bootstrap-controller", "--timestamps"}, tailArgs(opts.PostgresLogTail)...)...),
		},
	}
	if instance.PodName != "" {
		commands = append(commands,
			captureCommand{
				operation: "kubectl-capture-postgres-describe",
				args:      c.args(spec, "describe", "pod", instance.PodName),
			},
			captureCommand{
				operation: "kubectl-capture-postgres-log",
				args:      c.args(spec, append([]string{"logs", instance.PodName, "-c", "postgres", "--timestamps"}, tailArgs(opts.PostgresLogTail)...)...),
			},
			captureCommand{
				operation: "kubectl-capture-postgres-bootstrap-log",
				args:      c.args(spec, append([]string{"logs", instance.PodName, "-c", "bootstrap-controller", "--timestamps"}, tailArgs(opts.PostgresLogTail)...)...),
			},
		)
	}

	var evidence []model.EvidenceRecord
	var joined error
	for _, cmd := range commands {
		commandEvidence, err := c.bestEffortRun(ctx, cmd.operation, cmd.args)
		trimCommandEvidenceStdout(commandEvidence, cmd.stdoutTail)
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

func trimCommandEvidenceStdout(evidence []model.EvidenceRecord, maxLines int) {
	if maxLines <= 0 {
		return
	}
	for i := range evidence {
		if evidence[i].Command != nil {
			evidence[i].Command.Stdout = tailLines(evidence[i].Command.Stdout, maxLines)
		}
	}
}

func tailLines(value string, maxLines int) string {
	if maxLines <= 0 || value == "" {
		return value
	}
	hasFinalNewline := strings.HasSuffix(value, "\n")
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	if len(lines) <= maxLines {
		return value
	}
	result := strings.Join(lines[len(lines)-maxLines:], "\n")
	if hasFinalNewline {
		result += "\n"
	}
	return result
}

func fullRecoveryFailed(data []byte) (bool, string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return false, "", fmt.Errorf("parse CNPG full-recovery pods: %w", err)
	}
	for _, item := range list.Items {
		if item.Status.Phase == "Failed" {
			return true, item.Metadata.Name, nil
		}
	}
	return false, "", nil
}

func podReady(data []byte) (bool, error) {
	var pod struct {
		Status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &pod); err != nil {
		return false, fmt.Errorf("parse CNPG instance pod: %w", err)
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == "True" {
			return true, nil
		}
	}
	return false, nil
}
