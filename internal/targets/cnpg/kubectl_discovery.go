package cnpg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

type BackupResource struct {
	Name          string
	Cluster       string
	Phase         string
	Method        string
	PluginName    string
	PluginVersion string
	BackupID      string
	CreatedAt     time.Time
}

type PluginRecoverySource struct {
	PluginName  string
	ObjectStore string
	ServerName  string
	WALArchiver bool
}

func (c *KubectlClient) CompletedBackup(ctx context.Context, spec VerifyClusterSpec, name string) (BackupResource, []model.EvidenceRecord, error) {
	evidence, result, err := c.run(ctx, "kubectl-discover-cnpg-backups", c.args(spec, "get", "backups.postgresql.cnpg.io", "-o", "json"), nil, c.cfg.Timeout)
	if err != nil {
		return BackupResource{}, evidence, err
	}
	if !result.Evidence.ExitStatus.Success {
		return BackupResource{}, evidence, fmt.Errorf("kubectl-discover-cnpg-backups failed: %s", result.Evidence.ExitStatus.Summary())
	}

	backups, err := parseBackupResources(result.Raw.Stdout)
	if err != nil {
		return BackupResource{}, evidence, err
	}

	requestedName := strings.TrimSpace(name)
	if requestedName != "" {
		for _, backup := range backups {
			if backup.Name != requestedName {
				continue
			}
			if backup.Cluster != spec.SourceCluster {
				return BackupResource{}, evidence, fmt.Errorf("CNPG Backup %q belongs to cluster %q, not %q", requestedName, backup.Cluster, spec.SourceCluster)
			}
			if !strings.EqualFold(backup.Phase, "completed") {
				return BackupResource{}, evidence, fmt.Errorf("CNPG Backup %q phase is %q, not completed", requestedName, backup.Phase)
			}
			return backup, evidence, nil
		}
		return BackupResource{}, evidence, fmt.Errorf("CNPG Backup %q not found", requestedName)
	}

	var selected BackupResource
	for _, backup := range backups {
		if backup.Cluster != spec.SourceCluster || !strings.EqualFold(backup.Phase, "completed") {
			continue
		}
		if selected.Name == "" ||
			backup.CreatedAt.After(selected.CreatedAt) ||
			(backup.CreatedAt.Equal(selected.CreatedAt) && backup.Name > selected.Name) {
			selected = backup
		}
	}
	if selected.Name == "" {
		return BackupResource{}, evidence, fmt.Errorf("no completed CNPG Backup found for cluster %q", spec.SourceCluster)
	}
	return selected, evidence, nil
}

func (c *KubectlClient) LatestCompletedBackup(ctx context.Context, spec VerifyClusterSpec) (string, []model.EvidenceRecord, error) {
	backup, evidence, err := c.CompletedBackup(ctx, spec, "")
	if err != nil {
		return "", evidence, err
	}
	return backup.Name, evidence, nil
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
		podEvidence, podResult, podErr := c.run(ctx, "kubectl-discover-cnpg-source-pod-image", c.args(spec, "get", "pods", "-l", "cnpg.io/cluster="+spec.SourceCluster, "-o", "json"), nil, c.cfg.Timeout)
		evidence = append(evidence, podEvidence...)
		if podErr != nil {
			return "", evidence, podErr
		}
		if !podResult.Evidence.ExitStatus.Success {
			return "", evidence, fmt.Errorf("kubectl-discover-cnpg-source-pod-image failed: %s", podResult.Evidence.ExitStatus.Summary())
		}
		image, err = parsePostgresPodImage(podResult.Raw.Stdout)
		if err != nil {
			return "", evidence, err
		}
		if image == "" {
			return "", evidence, fmt.Errorf("CNPG Cluster %q has neither spec.imageName nor a postgres container image", spec.SourceCluster)
		}
	}
	return image, evidence, nil
}

func (c *KubectlClient) SourceClusterPlugin(ctx context.Context, spec VerifyClusterSpec, pluginName string) (PluginRecoverySource, []model.EvidenceRecord, error) {
	evidence, result, err := c.run(ctx, "kubectl-discover-cnpg-source-plugin", c.args(spec, "get", "cluster.postgresql.cnpg.io", spec.SourceCluster, "-o", "json"), nil, c.cfg.Timeout)
	if err != nil {
		return PluginRecoverySource{}, evidence, err
	}
	if !result.Evidence.ExitStatus.Success {
		return PluginRecoverySource{}, evidence, fmt.Errorf("kubectl-discover-cnpg-source-plugin failed: %s", result.Evidence.ExitStatus.Summary())
	}

	source, err := parseClusterPlugin(result.Raw.Stdout, pluginName, spec.SourceCluster)
	if err != nil {
		return PluginRecoverySource{}, evidence, err
	}
	return source, evidence, nil
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
				Method              string `json:"method"`
				PluginConfiguration struct {
					Name string `json:"name"`
				} `json:"pluginConfiguration"`
			} `json:"spec"`
			Status struct {
				Phase          string `json:"phase"`
				BackupID       string `json:"backupId"`
				PluginMetadata struct {
					Version string `json:"version"`
				} `json:"pluginMetadata"`
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
			Name:          item.Metadata.Name,
			Cluster:       item.Spec.Cluster.Name,
			Phase:         item.Status.Phase,
			Method:        item.Spec.Method,
			PluginName:    item.Spec.PluginConfiguration.Name,
			PluginVersion: item.Status.PluginMetadata.Version,
			BackupID:      item.Status.BackupID,
			CreatedAt:     createdAt,
		})
	}
	return backups, nil
}

func parseClusterPlugin(data []byte, pluginName, defaultServerName string) (PluginRecoverySource, error) {
	var cluster struct {
		Spec struct {
			Plugins []struct {
				Name          string            `json:"name"`
				Enabled       *bool             `json:"enabled"`
				IsWALArchiver bool              `json:"isWALArchiver"`
				Parameters    map[string]string `json:"parameters"`
			} `json:"plugins"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &cluster); err != nil {
		return PluginRecoverySource{}, fmt.Errorf("parse CNPG Cluster plugins: %w", err)
	}

	name := strings.TrimSpace(pluginName)
	for _, plugin := range cluster.Spec.Plugins {
		if plugin.Name != name {
			continue
		}
		if plugin.Enabled != nil && !*plugin.Enabled {
			return PluginRecoverySource{}, fmt.Errorf("CNPG source plugin %q is disabled", name)
		}
		objectStore := strings.TrimSpace(plugin.Parameters["barmanObjectName"])
		if objectStore == "" {
			return PluginRecoverySource{}, fmt.Errorf("CNPG source plugin %q has no barmanObjectName parameter", name)
		}
		serverName := strings.TrimSpace(plugin.Parameters["serverName"])
		if serverName == "" {
			serverName = strings.TrimSpace(defaultServerName)
		}
		return PluginRecoverySource{
			PluginName:  name,
			ObjectStore: objectStore,
			ServerName:  serverName,
			WALArchiver: plugin.IsWALArchiver,
		}, nil
	}
	return PluginRecoverySource{}, fmt.Errorf("CNPG source cluster has no plugin %q", name)
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

func parsePostgresPodImage(data []byte) (string, error) {
	var pods struct {
		Items []struct {
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &pods); err != nil {
		return "", fmt.Errorf("parse CNPG source pods: %w", err)
	}
	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			if container.Name == "postgres" && container.Image != "" {
				return container.Image, nil
			}
		}
	}
	return "", nil
}
