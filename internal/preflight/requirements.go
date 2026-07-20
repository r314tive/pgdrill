package preflight

import (
	"fmt"
	"strings"

	"github.com/r314tive/pgdrill/internal/adapters"
	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/probes"
)

type Requirement struct {
	Tool         model.ToolType
	Components   []string
	Binary       string
	Args         []string
	Env          map[string]string
	WorkDir      string
	RedactValues []string
}

func Requirements(cfg config.Config) ([]Requirement, error) {
	requirements := make([]Requirement, 0, 8)

	switch cfg.Target.Type {
	case model.RestoreTargetLocal:
		if err := adapters.ValidateConfig(cfg.Provider, cfg.Restore); err != nil {
			return nil, fmt.Errorf("validate provider config: %w", err)
		}
		provider, err := providerRequirement(cfg.Provider)
		if err != nil {
			return nil, err
		}
		requirements = append(requirements, provider)
		requirements = append(requirements, Requirement{
			Tool:         model.ToolPostgres,
			Components:   []string{"target.local"},
			Binary:       firstNonEmpty(cfg.Target.PostgresBinary, "postgres"),
			Args:         []string{"--version"},
			Env:          copyStringMap(cfg.Target.Env),
			RedactValues: append([]string{}, cfg.Target.RedactValues...),
		})
		if cfg.Restore.VerifyBackup.Enabled {
			requirements = append(requirements, Requirement{
				Tool:       model.ToolPGVerifyBackup,
				Components: []string{"restore.verify_backup"},
				Binary:     firstNonEmpty(cfg.Restore.VerifyBackup.Binary, "pg_verifybackup"),
				Args:       []string{"--version"},
				RedactValues: append(
					append([]string{}, cfg.Provider.RedactValues...),
					cfg.Restore.VerifyBackup.RedactValues...,
				),
			})
		}
	case model.RestoreTargetKubernetes:
		requirements = append(requirements, Requirement{
			Tool:       model.ToolKubectl,
			Components: []string{"target.kubernetes"},
			Binary:     firstNonEmpty(cfg.Target.Kubernetes.KubectlBinary, "kubectl"),
			Args:       []string{"version", "--client", "--output=json"},
			RedactValues: append(
				append([]string{}, cfg.Target.RedactValues...),
				cfg.Provider.RedactValues...,
			),
		})
	case model.RestoreTargetContainer:
		return nil, fmt.Errorf("restore target %q is not implemented", cfg.Target.Type)
	default:
		return nil, fmt.Errorf("unsupported restore target %q", cfg.Target.Type)
	}

	expandedProbes, err := probes.ResolveConfigs(cfg.Probes)
	if err != nil {
		return nil, fmt.Errorf("validate probes: %w", err)
	}
	for _, probe := range expandedProbes {
		requirement, err := probeRequirement(probe)
		if err != nil {
			return nil, err
		}
		requirements = append(requirements, requirement)
	}

	return mergeRequirements(requirements), nil
}

func providerRequirement(cfg config.ProviderConfig) (Requirement, error) {
	requirement := Requirement{
		Components:   []string{"provider." + string(cfg.Type)},
		Env:          copyStringMap(cfg.Env),
		WorkDir:      cfg.WorkDir,
		RedactValues: append([]string{}, cfg.RedactValues...),
	}
	switch cfg.Type {
	case model.ProviderWALG:
		requirement.Tool = model.ToolWALG
		requirement.Binary = firstNonEmpty(cfg.Binary, "wal-g")
		requirement.Args = []string{"--version"}
	case model.ProviderBarman:
		requirement.Tool = model.ToolBarman
		requirement.Binary = firstNonEmpty(cfg.Binary, "barman")
		requirement.Args = []string{"--version"}
	case model.ProviderPGBackRest:
		requirement.Tool = model.ToolPGBackRest
		requirement.Binary = firstNonEmpty(cfg.Binary, "pgbackrest")
		requirement.Args = []string{"version"}
	case model.ProviderPGProbackup:
		requirement.Tool = model.ToolPGProbackup
		requirement.Binary = firstNonEmpty(cfg.Binary, "pg_probackup")
		requirement.Args = []string{"version"}
	default:
		return Requirement{}, fmt.Errorf("provider %q is not implemented", cfg.Type)
	}
	return requirement, nil
}

func probeRequirement(cfg config.ProbeConfig) (Requirement, error) {
	componentName := cfg.Name
	if componentName == "" {
		componentName = string(cfg.Type)
	}
	requirement := Requirement{
		Components:   []string{"probe." + componentName},
		RedactValues: append([]string{}, cfg.RedactValues...),
	}
	switch cfg.Type {
	case model.ProbePGIsReady:
		requirement.Tool = model.ToolPGIsReady
		requirement.Binary = firstNonEmpty(cfg.Binary, "pg_isready")
	case model.ProbeSQL:
		requirement.Tool = model.ToolPSQL
		requirement.Binary = firstNonEmpty(cfg.Binary, "psql")
	case model.ProbeAMCheck:
		requirement.Tool = model.ToolPGAMCheck
		requirement.Binary = firstNonEmpty(cfg.Binary, "pg_amcheck")
	case model.ProbePGDump:
		requirement.Tool = model.ToolPGDump
		requirement.Binary = firstNonEmpty(cfg.Binary, "pg_dump")
	default:
		return Requirement{}, fmt.Errorf("probe %q is not implemented", cfg.Type)
	}
	requirement.Args = []string{"--version"}
	return requirement, nil
}

func mergeRequirements(requirements []Requirement) []Requirement {
	merged := make([]Requirement, 0, len(requirements))
	indexes := map[string]int{}
	for _, requirement := range requirements {
		key := string(requirement.Tool) + "\x00" + requirement.Binary + "\x00" + strings.Join(requirement.Args, "\x00")
		if index, ok := indexes[key]; ok {
			for _, component := range requirement.Components {
				merged[index].Components = appendUnique(merged[index].Components, component)
			}
			merged[index].RedactValues = appendUnique(merged[index].RedactValues, requirement.RedactValues...)
			continue
		}
		indexes[key] = len(merged)
		merged = append(merged, requirement)
	}
	return merged
}

func appendUnique(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
