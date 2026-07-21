package cnpg

import (
	"context"
	"fmt"

	"github.com/r314tive/pgdrill/internal/model"
)

// VerifyTarget adapts the CNPG controller to the core managed-restore target
// contract. The controller retains ownership of operator-specific create,
// wait, diagnostic, and cleanup behavior.
type VerifyTarget struct {
	Spec       VerifyClusterSpec
	controller *Controller
	attempt    model.AttemptContext
	operation  model.Operation
}

func NewVerifyTarget(spec VerifyClusterSpec, client Client, options LifecycleOptions) *VerifyTarget {
	return &VerifyTarget{
		Spec: spec,
		controller: &Controller{
			Spec:    spec,
			Client:  client,
			Options: options,
		},
	}
}

func (t *VerifyTarget) Type() model.RestoreTargetType {
	return model.RestoreTargetKubernetes
}

func (t *VerifyTarget) BindAttempt(attempt model.AttemptContext) error {
	if err := attempt.Validate(); err != nil {
		return fmt.Errorf("validate CNPG target attempt: %w", err)
	}
	if attempt.Target.Type != model.RestoreTargetKubernetes {
		return fmt.Errorf("CNPG target cannot bind target type %q", attempt.Target.Type)
	}
	ownerID, err := attempt.Identity.OwnershipID()
	if err != nil {
		return fmt.Errorf("derive CNPG ownership id: %w", err)
	}
	if t.Spec.OwnershipID != ownerID {
		return fmt.Errorf("CNPG ownership id %q does not match attempt-derived id %q", t.Spec.OwnershipID, ownerID)
	}
	t.attempt = attempt
	t.operation = model.Operation{}
	return nil
}

func (t *VerifyTarget) BeginOperation(operation model.Operation) error {
	if err := operation.Validate(); err != nil {
		return fmt.Errorf("validate CNPG target operation: %w", err)
	}
	if err := t.attempt.Validate(); err != nil {
		return fmt.Errorf("CNPG target attempt is not bound: %w", err)
	}
	if operation.Identity != t.attempt.Identity {
		return fmt.Errorf("operation attempt identity does not match CNPG target binding")
	}
	if operation.Kind != model.OperationManagedStart && operation.Kind != model.OperationTargetCleanup {
		return fmt.Errorf("CNPG target cannot execute operation kind %q", operation.Kind)
	}
	t.operation = operation
	return nil
}

func (t *VerifyTarget) Reconcile(ctx context.Context, checkpoint model.OperationCheckpoint) (model.OperationReconciliation, error) {
	if err := checkpoint.Validate(); err != nil {
		return model.OperationReconciliation{}, fmt.Errorf("validate CNPG checkpoint: %w", err)
	}
	if checkpoint.Operation.Key != t.operation.Key {
		return model.OperationReconciliation{}, fmt.Errorf("checkpoint operation does not match active CNPG target operation")
	}
	owned, evidence, err := t.controller.Client.FindOwnedCluster(ctx, t.Spec)
	if err != nil {
		return model.OperationReconciliation{Evidence: evidence}, err
	}
	if owned.Found && owned.Name != t.Spec.Name {
		return model.OperationReconciliation{
			Disposition: model.ReconciliationConflict,
			Message:     fmt.Sprintf("ownership id matches unexpected CNPG cluster %q", owned.Name),
			Evidence:    evidence,
		}, nil
	}

	switch checkpoint.Operation.Kind {
	case model.OperationManagedStart:
		if !owned.Found {
			return model.OperationReconciliation{
				Disposition: model.ReconciliationNotApplied,
				Message:     "no CNPG cluster has the attempt ownership id",
				Evidence:    evidence,
			}, nil
		}
		t.controller.created = true
		instance, waitEvidence, waitErr := t.controller.Client.WaitForInstanceReady(ctx, t.Spec, WaitOptions{
			Timeout:      t.controller.Options.WaitTimeout,
			PollInterval: t.controller.Options.PollInterval,
		})
		evidence = append(evidence, waitEvidence...)
		if waitErr != nil {
			return model.OperationReconciliation{
				Disposition: model.ReconciliationUnknown,
				Message:     "owned CNPG cluster exists but readiness is not proven",
				Evidence:    evidence,
			}, nil
		}
		t.controller.instance = instance
		pg := runningPostgres(instance)
		return model.OperationReconciliation{
			Disposition: model.ReconciliationCompleted,
			Message:     "owned CNPG cluster and Ready instance prove target startup",
			Postgres:    &pg,
			Report: model.CheckReport{Checks: []model.Check{{
				Name:    "cnpg-instance-ready",
				Status:  model.CheckStatusPassed,
				Message: "CNPG verify cluster is Ready after reconciliation",
				Attributes: map[string]string{
					"backup":         t.Spec.BackupName,
					"instance_pod":   t.Spec.InstancePodName,
					"postgres_host":  pg.Host,
					"source_cluster": t.Spec.SourceCluster,
					"verify_cluster": t.Spec.Name,
				},
			}}, Evidence: evidence},
		}, nil
	case model.OperationTargetCleanup:
		if !owned.Found {
			t.controller.created = false
			return model.OperationReconciliation{
				Disposition: model.ReconciliationCompleted,
				Message:     "no CNPG cluster has the attempt ownership id",
				Evidence:    evidence,
			}, nil
		}
		t.controller.created = true
		return model.OperationReconciliation{
			Disposition: model.ReconciliationNotApplied,
			Message:     "owned CNPG cluster still requires cleanup",
			Evidence:    evidence,
		}, nil
	default:
		return model.OperationReconciliation{}, fmt.Errorf("CNPG target cannot reconcile operation kind %q", checkpoint.Operation.Kind)
	}
}

func (t *VerifyTarget) Start(ctx context.Context) (model.RunningPostgres, model.CheckReport, error) {
	if t.operation.Kind != model.OperationManagedStart {
		return model.RunningPostgres{}, model.CheckReport{}, fmt.Errorf("CNPG target start operation is not bound")
	}
	pg, evidence, err := t.controller.Start(ctx)
	status := model.CheckStatusPassed
	message := "CNPG verify cluster is Ready"
	if err != nil {
		status = model.CheckStatusFailed
		message = err.Error()
	}
	return pg, model.CheckReport{
		Checks: []model.Check{{
			Name:    "cnpg-instance-ready",
			Status:  status,
			Message: message,
			Attributes: map[string]string{
				"backup":         t.Spec.BackupName,
				"instance_pod":   t.Spec.InstancePodName,
				"postgres_host":  pg.Host,
				"source_cluster": t.Spec.SourceCluster,
				"verify_cluster": t.Spec.Name,
			},
		}},
		Evidence: evidence,
	}, err
}

func (t *VerifyTarget) Destroy(ctx context.Context) ([]model.EvidenceRecord, error) {
	if t.operation.Kind != model.OperationTargetCleanup {
		return nil, fmt.Errorf("CNPG target cleanup operation is not bound")
	}
	return t.controller.Destroy(ctx)
}

func runningPostgres(instance Instance) model.RunningPostgres {
	return model.RunningPostgres{
		ConnString: instance.ConnString,
		Host:       instance.Host,
		Port:       instance.Port,
	}
}
