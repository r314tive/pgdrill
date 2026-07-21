package model

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const CurrentDrillSpecSchemaVersion = "pgdrill.drill-spec/v1alpha1"

type DrillMode string

const (
	DrillModeNative  DrillMode = "native"
	DrillModeManaged DrillMode = "managed"
)

func (m DrillMode) IsKnown() bool {
	switch m {
	case DrillModeNative, DrillModeManaged:
		return true
	default:
		return false
	}
}

type BackupSelectionType string

const (
	BackupSelectionLatestAvailable BackupSelectionType = "latest_available"
	BackupSelectionByID            BackupSelectionType = "backup_id"
)

func (t BackupSelectionType) IsKnown() bool {
	switch t {
	case BackupSelectionLatestAvailable, BackupSelectionByID:
		return true
	default:
		return false
	}
}

type BackupSelection struct {
	Type     BackupSelectionType `json:"type"`
	BackupID string              `json:"backup_id,omitempty"`
}

type ComponentRef struct {
	ID       string `json:"id"`
	Driver   string `json:"driver"`
	Revision string `json:"revision"`
}

type BackupSourceSpec struct {
	Ref      ComponentRef `json:"ref"`
	Provider ProviderType `json:"provider,omitempty"`
}

type RestoreTargetSpec struct {
	Ref  ComponentRef `json:"ref"`
	Spec TargetSpec   `json:"spec"`
}

type ProbeDescriptor struct {
	Type ProbeType `json:"type"`
	Name string    `json:"name"`
}

type ProbeProfileSpec struct {
	Ref    ComponentRef      `json:"ref"`
	Probes []ProbeDescriptor `json:"probes"`
}

// DrillSpec is a secret-free snapshot of one engine input. Run and attempt
// identities are deliberately stored outside the spec so retries preserve the
// same digest while receiving a distinct attempt identity.
type DrillSpec struct {
	SchemaVersion   string            `json:"schema_version"`
	Mode            DrillMode         `json:"mode"`
	Cluster         string            `json:"cluster"`
	Source          BackupSourceSpec  `json:"source"`
	BackupSelection BackupSelection   `json:"backup_selection"`
	Target          RestoreTargetSpec `json:"target"`
	RecoveryTarget  RecoveryTarget    `json:"recovery_target"`
	Policy          RecoveryPolicy    `json:"policy"`
	ProbeProfile    ProbeProfileSpec  `json:"probe_profile"`
}

func DefaultProbeName(probeType ProbeType) string {
	switch probeType {
	case ProbePGIsReady:
		return "pg_isready"
	case ProbeSQL:
		return "sql"
	case ProbeAMCheck:
		return "pg_amcheck"
	case ProbePGDump:
		return "pg_dump"
	default:
		return strings.TrimSpace(string(probeType))
	}
}

func IsSHA256Digest(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+sha256.Size*2 || !strings.HasPrefix(value, prefix) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil
}
