package cnpg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

type BackupResource struct {
	Name      string
	Cluster   string
	Phase     string
	CreatedAt time.Time
}

func (c *KubectlClient) LatestCompletedBackup(ctx context.Context, spec VerifyClusterSpec) (string, []model.EvidenceRecord, error) {
	evidence, result, err := c.run(ctx, "kubectl-discover-cnpg-backups", c.args(spec, "get", "backups.postgresql.cnpg.io", "-o", "json"), nil, c.cfg.Timeout)
	if err != nil {
		return "", evidence, err
	}
	if !result.Evidence.ExitStatus.Success {
		return "", evidence, fmt.Errorf("kubectl-discover-cnpg-backups failed: %s", result.Evidence.ExitStatus.Summary())
	}

	backups, err := parseBackupResources(result.Raw.Stdout)
	if err != nil {
		return "", evidence, err
	}

	var selected BackupResource
	for _, backup := range backups {
		if backup.Cluster != spec.SourceCluster || backup.Phase != "completed" {
			continue
		}
		if selected.Name == "" || backup.CreatedAt.After(selected.CreatedAt) {
			selected = backup
		}
	}
	if selected.Name == "" {
		return "", evidence, fmt.Errorf("no completed CNPG Backup found for cluster %q", spec.SourceCluster)
	}
	return selected.Name, evidence, nil
}

func (c *KubectlClient) SourceClusterImage(ctx context.Context, spec VerifyClusterSpec) (string, []model.EvidenceRecord, error) {
	evidence, result, err := c.run(ctx, "kubectl-discover-cnpg-source-image", c.args(spec, "get", "cluster.postgresql.cnpg.io", spec.SourceCluster, "-o", "json"), nil, c.cfg.Timeout)
	if err != nil {
		return "", evidence, err
	}
	if !result.Evidence.ExitStatus.Success {
		return "", evidence, fmt.Errorf("kubectl-discover-cnpg-source-image failed: %s", result.Evidence.ExitStatus.Summary())
	}

	image, err := parseClusterImage(result.Raw.Stdout)
	if err != nil {
		return "", evidence, err
	}
	if image == "" {
		return "", evidence, fmt.Errorf("CNPG Cluster %q does not report spec.imageName", spec.SourceCluster)
	}
	return image, evidence, nil
}

func parseBackupResources(data []byte) ([]BackupResource, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string `json:"name"`
				CreationTimestamp string `json:"creationTimestamp"`
			} `json:"metadata"`
			Spec struct {
				Cluster struct {
					Name string `json:"name"`
				} `json:"cluster"`
			} `json:"spec"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse CNPG Backup list: %w", err)
	}

	backups := make([]BackupResource, 0, len(list.Items))
	for _, item := range list.Items {
		createdAt, err := time.Parse(time.RFC3339, item.Metadata.CreationTimestamp)
		if err != nil && item.Metadata.CreationTimestamp != "" {
			return nil, fmt.Errorf("parse CNPG Backup creationTimestamp %q: %w", item.Metadata.CreationTimestamp, err)
		}
		backups = append(backups, BackupResource{
			Name:      item.Metadata.Name,
			Cluster:   item.Spec.Cluster.Name,
			Phase:     item.Status.Phase,
			CreatedAt: createdAt,
		})
	}
	return backups, nil
}

func parseClusterImage(data []byte) (string, error) {
	var cluster struct {
		Spec struct {
			ImageName string `json:"imageName"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &cluster); err != nil {
		return "", fmt.Errorf("parse CNPG Cluster: %w", err)
	}
	return cluster.Spec.ImageName, nil
}
