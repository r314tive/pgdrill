package cnpg

import (
	"context"

	"github.com/r314tive/pgdrill/internal/model"
)

// VerifyTarget adapts the CNPG controller to the core managed-restore target
// contract. The controller retains ownership of operator-specific create,
// wait, diagnostic, and cleanup behavior.
type VerifyTarget struct {
	Spec       VerifyClusterSpec
	controller *Controller
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

func (t *VerifyTarget) Start(ctx context.Context) (model.RunningPostgres, model.CheckReport, error) {
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
	return t.controller.Destroy(ctx)
}
