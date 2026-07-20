package cnpg

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/r314tive/pgdrill/internal/finalize"
	"github.com/r314tive/pgdrill/internal/model"
)

const (
	DefaultWaitTimeout  = 2 * time.Hour
	DefaultPollInterval = 5 * time.Second
	DefaultPostgresPort = 5432
)

type Client interface {
	CreateCluster(ctx context.Context, spec VerifyClusterSpec, manifest []byte) ([]model.EvidenceRecord, error)
	WaitForInstanceReady(ctx context.Context, spec VerifyClusterSpec, opts WaitOptions) (Instance, []model.EvidenceRecord, error)
	CaptureEvidence(ctx context.Context, spec VerifyClusterSpec, instance Instance, opts CaptureOptions) ([]model.EvidenceRecord, error)
	DeleteCluster(ctx context.Context, spec VerifyClusterSpec) ([]model.EvidenceRecord, error)
	DeletePVCs(ctx context.Context, spec VerifyClusterSpec) ([]model.EvidenceRecord, error)
}

type LifecycleOptions struct {
	WaitTimeout         time.Duration
	PollInterval        time.Duration
	CleanupPVC          bool
	CleanupOnFail       bool
	CaptureLogs         bool
	EventsTail          int
	PostgresLogTail     int
	FinalizationTimeout time.Duration
	Clock               func() time.Time
}

type WaitOptions struct {
	Timeout      time.Duration
	PollInterval time.Duration
}

type CaptureOptions struct {
	Reason          string
	EventsTail      int
	PostgresLogTail int
}

type Instance struct {
	PodName    string
	Host       string
	Port       int
	Database   string
	ConnString string
}

type Controller struct {
	Spec     VerifyClusterSpec
	Client   Client
	Options  LifecycleOptions
	created  bool
	instance Instance
}

func (c *Controller) Start(ctx context.Context) (model.RunningPostgres, []model.EvidenceRecord, error) {
	if c.Client == nil {
		return model.RunningPostgres{}, nil, fmt.Errorf("cnpg client is required")
	}

	manifest, err := c.Spec.ManifestYAML()
	if err != nil {
		return model.RunningPostgres{}, nil, err
	}

	evidence := []model.EvidenceRecord{c.runtimeEvidence("cnpg-manifest-render", map[string]string{
		"backup":             c.Spec.BackupName,
		"bytes":              strconv.Itoa(len(manifest)),
		"cluster":            c.Spec.Name,
		"full_recovery_job":  c.Spec.FullRecoveryJob,
		"instance_pod":       c.Spec.InstancePodName,
		"namespace":          c.Spec.Namespace,
		"ownership_id":       c.Spec.OwnershipID,
		"source_cluster":     c.Spec.SourceCluster,
		"storage_size":       c.Spec.StorageSize,
		"postgres_image":     c.Spec.ImageName,
		"target_port":        strconv.Itoa(DefaultPostgresPort),
		"verify_cluster_uid": c.Spec.Name,
	})}

	createEvidence, err := c.Client.CreateCluster(ctx, c.Spec, manifest)
	evidence = append(evidence, createEvidence...)
	if err != nil {
		c.created = createMayHaveSucceeded(createEvidence)
		if !c.created {
			return model.RunningPostgres{}, evidence, fmt.Errorf("create cnpg verify cluster: %w", err)
		}
		evidence, err = c.finalizeCreateFailure(ctx, evidence, err)
		return model.RunningPostgres{}, evidence, fmt.Errorf("create cnpg verify cluster: %w", err)
	}
	c.created = true

	instance, waitEvidence, err := c.Client.WaitForInstanceReady(ctx, c.Spec, c.waitOptions())
	evidence = append(evidence, waitEvidence...)
	if err != nil {
		evidence, err = c.finalizeStartFailure(ctx, evidence, err, "start-failed")
		return model.RunningPostgres{}, evidence, fmt.Errorf("wait for cnpg verify cluster: %w", err)
	}

	if instance.PodName == "" {
		instance.PodName = c.Spec.InstancePodName
	}
	if instance.Port == 0 {
		instance.Port = DefaultPostgresPort
	}
	c.instance = instance

	return model.RunningPostgres{
		ConnString: instance.ConnString,
		Host:       instance.Host,
		Port:       instance.Port,
	}, evidence, nil
}

func createMayHaveSucceeded(evidence []model.EvidenceRecord) bool {
	for i := len(evidence) - 1; i >= 0; i-- {
		if evidence[i].Command == nil {
			continue
		}
		status := evidence[i].Command.ExitStatus
		if !status.Started {
			return false
		}
		return true
	}
	return true
}

func (c *Controller) finalizeCreateFailure(ctx context.Context, evidence []model.EvidenceRecord, failure error) ([]model.EvidenceRecord, error) {
	if !c.Options.CleanupOnFail {
		return evidence, failure
	}
	cleanupCtx, cancel := finalize.Context(ctx, c.Options.FinalizationTimeout)
	cleanupEvidence, cleanupErr := c.cleanup(cleanupCtx)
	cancel()
	return append(evidence, cleanupEvidence...), errors.Join(failure, cleanupErr)
}

func (c *Controller) finalizeStartFailure(ctx context.Context, evidence []model.EvidenceRecord, failure error, reason string) ([]model.EvidenceRecord, error) {
	if c.Options.CaptureLogs {
		captureCtx, cancel := finalize.Context(ctx, c.Options.FinalizationTimeout)
		captureEvidence, captureErr := c.Client.CaptureEvidence(captureCtx, c.Spec, Instance{
			PodName: c.Spec.InstancePodName,
			Port:    DefaultPostgresPort,
		}, c.captureOptions(reason))
		cancel()
		evidence = append(evidence, captureEvidence...)
		failure = errors.Join(failure, captureErr)
	}
	if c.Options.CleanupOnFail {
		cleanupCtx, cancel := finalize.Context(ctx, c.Options.FinalizationTimeout)
		cleanupEvidence, cleanupErr := c.cleanup(cleanupCtx)
		cancel()
		evidence = append(evidence, cleanupEvidence...)
		failure = errors.Join(failure, cleanupErr)
	}
	return evidence, failure
}

func (c *Controller) Destroy(ctx context.Context) ([]model.EvidenceRecord, error) {
	if !c.created {
		return nil, nil
	}

	evidence := []model.EvidenceRecord{}
	var err error
	if c.Options.CaptureLogs {
		captureEvidence, captureErr := c.Client.CaptureEvidence(ctx, c.Spec, c.instance, c.captureOptions("destroy"))
		evidence = append(evidence, captureEvidence...)
		err = errors.Join(err, captureErr)
	}

	cleanupEvidence, cleanupErr := c.cleanup(ctx)
	evidence = append(evidence, cleanupEvidence...)
	err = errors.Join(err, cleanupErr)
	return evidence, err
}

func (c *Controller) cleanup(ctx context.Context) ([]model.EvidenceRecord, error) {
	evidence := []model.EvidenceRecord{}
	var err error

	clusterEvidence, clusterErr := c.Client.DeleteCluster(ctx, c.Spec)
	evidence = append(evidence, clusterEvidence...)
	err = errors.Join(err, clusterErr)

	if c.Options.CleanupPVC {
		pvcEvidence, pvcErr := c.Client.DeletePVCs(ctx, c.Spec)
		evidence = append(evidence, pvcEvidence...)
		err = errors.Join(err, pvcErr)
	}

	if err == nil {
		c.created = false
	}
	return evidence, err
}

func (c *Controller) waitOptions() WaitOptions {
	timeout := c.Options.WaitTimeout
	if timeout == 0 {
		timeout = DefaultWaitTimeout
	}
	poll := c.Options.PollInterval
	if poll == 0 {
		poll = DefaultPollInterval
	}
	return WaitOptions{
		Timeout:      timeout,
		PollInterval: poll,
	}
}

func (c *Controller) captureOptions(reason string) CaptureOptions {
	return CaptureOptions{
		Reason:          reason,
		EventsTail:      c.Options.EventsTail,
		PostgresLogTail: c.Options.PostgresLogTail,
	}
}

func (c *Controller) runtimeEvidence(operation string, attributes map[string]string) model.EvidenceRecord {
	now := time.Now().UTC()
	if c.Options.Clock != nil {
		now = c.Options.Clock().UTC()
	}
	attributes["operation"] = operation
	return model.EvidenceRecord{
		ID:          "cnpg:" + operation + ":" + now.Format(time.RFC3339Nano),
		Kind:        model.EvidenceRuntime,
		Source:      string(model.RestoreTargetKubernetes),
		CollectedAt: now,
		Attributes:  attributes,
	}
}
