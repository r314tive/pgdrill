package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const CurrentOperationCheckpointSchemaVersion = "pgdrill.operation-checkpoint/v1alpha1"

const (
	maxOperationNameBytes    = 256
	maxOperationMessageBytes = 4096
)

// AttemptIdentity is the immutable identity shared by every mutation in one
// engine attempt. OwnershipID is deterministic so an executor can locate its
// resources after losing in-memory state.
type AttemptIdentity struct {
	RunID      string `json:"run_id"`
	AttemptID  string `json:"attempt_id"`
	SpecDigest string `json:"spec_digest"`
}

func (i AttemptIdentity) Validate() error {
	if strings.TrimSpace(i.RunID) == "" {
		return fmt.Errorf("run_id is required")
	}
	if i.RunID != strings.TrimSpace(i.RunID) {
		return fmt.Errorf("run_id must not contain surrounding whitespace")
	}
	if strings.TrimSpace(i.AttemptID) == "" {
		return fmt.Errorf("attempt_id is required")
	}
	if i.AttemptID != strings.TrimSpace(i.AttemptID) {
		return fmt.Errorf("attempt_id must not contain surrounding whitespace")
	}
	if !IsSHA256Digest(i.SpecDigest) {
		return fmt.Errorf("spec_digest must be a sha256 digest")
	}
	return nil
}

func (i AttemptIdentity) OwnershipID() (string, error) {
	if err := i.Validate(); err != nil {
		return "", fmt.Errorf("validate attempt identity: %w", err)
	}
	payload, err := json.Marshal(struct {
		Domain   string          `json:"domain"`
		Identity AttemptIdentity `json:"identity"`
	}{
		Domain:   "pgdrill.attempt-ownership/v1",
		Identity: i,
	})
	if err != nil {
		return "", fmt.Errorf("encode attempt ownership identity: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:16]), nil
}

type AttemptContext struct {
	Identity AttemptIdentity `json:"identity"`
	Target   TargetSpec      `json:"target"`
}

func (c AttemptContext) Validate() error {
	if err := c.Identity.Validate(); err != nil {
		return fmt.Errorf("invalid identity: %w", err)
	}
	if !c.Target.Type.IsKnown() {
		return fmt.Errorf("target type %q is unsupported", c.Target.Type)
	}
	return nil
}

type OperationKind string

const (
	OperationTargetPrepare OperationKind = "target_prepare"
	OperationRestoreStep   OperationKind = "restore_step"
	OperationPostgresStart OperationKind = "postgres_start"
	OperationManagedStart  OperationKind = "managed_target_start"
	OperationTargetCleanup OperationKind = "target_cleanup"
)

func (k OperationKind) IsKnown() bool {
	switch k {
	case OperationTargetPrepare, OperationRestoreStep, OperationPostgresStart, OperationManagedStart, OperationTargetCleanup:
		return true
	default:
		return false
	}
}

type Operation struct {
	Key      string          `json:"key"`
	Identity AttemptIdentity `json:"identity"`
	Stage    DrillStage      `json:"stage"`
	Kind     OperationKind   `json:"kind"`
	Name     string          `json:"name"`
	Ordinal  int             `json:"ordinal"`
}

func NewOperation(identity AttemptIdentity, stage DrillStage, kind OperationKind, name string, ordinal int) (Operation, error) {
	operation := Operation{
		Identity: identity,
		Stage:    stage,
		Kind:     kind,
		Name:     strings.TrimSpace(name),
		Ordinal:  ordinal,
	}
	if err := operation.validateWithoutKey(); err != nil {
		return Operation{}, err
	}
	payload, err := json.Marshal(struct {
		Domain   string          `json:"domain"`
		Identity AttemptIdentity `json:"identity"`
		Stage    DrillStage      `json:"stage"`
		Kind     OperationKind   `json:"kind"`
		Name     string          `json:"name"`
		Ordinal  int             `json:"ordinal"`
	}{
		Domain:   "pgdrill.operation/v1",
		Identity: operation.Identity,
		Stage:    operation.Stage,
		Kind:     operation.Kind,
		Name:     operation.Name,
		Ordinal:  operation.Ordinal,
	})
	if err != nil {
		return Operation{}, fmt.Errorf("encode operation identity: %w", err)
	}
	digest := sha256.Sum256(payload)
	operation.Key = "sha256:" + hex.EncodeToString(digest[:])
	return operation, nil
}

func (o Operation) Validate() error {
	if err := o.validateWithoutKey(); err != nil {
		return err
	}
	if !IsSHA256Digest(o.Key) {
		return fmt.Errorf("key must be a sha256 digest")
	}
	want, err := NewOperation(o.Identity, o.Stage, o.Kind, o.Name, o.Ordinal)
	if err != nil {
		return err
	}
	if o.Key != want.Key {
		return fmt.Errorf("key %q does not match canonical operation key %q", o.Key, want.Key)
	}
	return nil
}

func (o Operation) validateWithoutKey() error {
	if err := o.Identity.Validate(); err != nil {
		return fmt.Errorf("invalid identity: %w", err)
	}
	if !o.Stage.IsKnown() {
		return fmt.Errorf("stage %q is unsupported", o.Stage)
	}
	if !o.Kind.IsKnown() {
		return fmt.Errorf("kind %q is unsupported", o.Kind)
	}
	if strings.TrimSpace(o.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if o.Name != strings.TrimSpace(o.Name) {
		return fmt.Errorf("name must not contain surrounding whitespace")
	}
	if len(o.Name) > maxOperationNameBytes {
		return fmt.Errorf("name exceeds %d bytes", maxOperationNameBytes)
	}
	if !utf8.ValidString(o.Name) {
		return fmt.Errorf("name must be valid UTF-8")
	}
	if o.Ordinal < 0 {
		return fmt.Errorf("ordinal must not be negative")
	}
	expectedStage := map[OperationKind]DrillStage{
		OperationTargetPrepare: DrillStageTargetPreparation,
		OperationRestoreStep:   DrillStageRestoreExecution,
		OperationPostgresStart: DrillStagePostgresStart,
		OperationManagedStart:  DrillStageTargetStart,
		OperationTargetCleanup: DrillStageTargetCleanup,
	}[o.Kind]
	if o.Stage != expectedStage {
		return fmt.Errorf("kind %q requires stage %q, got %q", o.Kind, expectedStage, o.Stage)
	}
	return nil
}

type OperationState string

const (
	OperationStateIntent    OperationState = "intent"
	OperationStateSucceeded OperationState = "succeeded"
	OperationStateFailed    OperationState = "failed"
	OperationStateUnknown   OperationState = "unknown"
)

func (s OperationState) IsKnown() bool {
	switch s {
	case OperationStateIntent, OperationStateSucceeded, OperationStateFailed, OperationStateUnknown:
		return true
	default:
		return false
	}
}

func (s OperationState) IsTerminal() bool {
	return s == OperationStateSucceeded || s == OperationStateFailed || s == OperationStateUnknown
}

type OperationCheckpoint struct {
	SchemaVersion string         `json:"schema_version"`
	Operation     Operation      `json:"operation"`
	State         OperationState `json:"state"`
	StartedAt     time.Time      `json:"started_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	Reconciled    bool           `json:"reconciled,omitempty"`
	Message       string         `json:"message,omitempty"`
}

func (c OperationCheckpoint) Validate() error {
	if c.SchemaVersion != CurrentOperationCheckpointSchemaVersion {
		return fmt.Errorf("schema_version must be %q", CurrentOperationCheckpointSchemaVersion)
	}
	if err := c.Operation.Validate(); err != nil {
		return fmt.Errorf("invalid operation: %w", err)
	}
	if !c.State.IsKnown() {
		return fmt.Errorf("state %q is unsupported", c.State)
	}
	if c.StartedAt.IsZero() {
		return fmt.Errorf("started_at is required")
	}
	if c.UpdatedAt.IsZero() {
		return fmt.Errorf("updated_at is required")
	}
	if c.UpdatedAt.Before(c.StartedAt) {
		return fmt.Errorf("updated_at must not be earlier than started_at")
	}
	if len(c.Message) > maxOperationMessageBytes {
		return fmt.Errorf("message exceeds %d bytes", maxOperationMessageBytes)
	}
	if !utf8.ValidString(c.Message) {
		return fmt.Errorf("message must be valid UTF-8")
	}
	return nil
}

type ReconciliationDisposition string

const (
	ReconciliationCompleted  ReconciliationDisposition = "completed"
	ReconciliationNotApplied ReconciliationDisposition = "not_applied"
	ReconciliationUnknown    ReconciliationDisposition = "unknown"
	ReconciliationConflict   ReconciliationDisposition = "conflict"
)

func (d ReconciliationDisposition) IsKnown() bool {
	switch d {
	case ReconciliationCompleted, ReconciliationNotApplied, ReconciliationUnknown, ReconciliationConflict:
		return true
	default:
		return false
	}
}

// OperationReconciliation is a bounded result of observing target state. It
// may reconstruct outputs needed when a mutation completed despite an
// uncertain command result.
type OperationReconciliation struct {
	Disposition ReconciliationDisposition `json:"disposition"`
	Message     string                    `json:"message,omitempty"`
	Postgres    *RunningPostgres          `json:"postgres,omitempty"`
	Report      CheckReport               `json:"report,omitempty"`
	Evidence    []EvidenceRecord          `json:"evidence,omitempty"`
}

func (r OperationReconciliation) Validate() error {
	if !r.Disposition.IsKnown() {
		return fmt.Errorf("disposition %q is unsupported", r.Disposition)
	}
	if len(r.Message) > maxOperationMessageBytes {
		return fmt.Errorf("message exceeds %d bytes", maxOperationMessageBytes)
	}
	return nil
}
