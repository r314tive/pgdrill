package model

import (
	"fmt"
	"strings"
	"time"
)

const CurrentRunEventSchemaVersion = "pgdrill.run-event/v1alpha1"

type RunEventType string

const (
	RunEventStarted        RunEventType = "run_started"
	RunEventStageStarted   RunEventType = "stage_started"
	RunEventStageCompleted RunEventType = "stage_completed"
	RunEventFinished       RunEventType = "run_finished"
)

func (t RunEventType) IsKnown() bool {
	switch t {
	case RunEventStarted, RunEventStageStarted, RunEventStageCompleted, RunEventFinished:
		return true
	default:
		return false
	}
}

type StageOutcome string

const (
	StageOutcomeSucceeded StageOutcome = "succeeded"
	StageOutcomeFailed    StageOutcome = "failed"
	StageOutcomeAborted   StageOutcome = "aborted"
)

func (o StageOutcome) IsTerminal() bool {
	switch o {
	case StageOutcomeSucceeded, StageOutcomeFailed, StageOutcomeAborted:
		return true
	default:
		return false
	}
}

type RunEvent struct {
	SchemaVersion string            `json:"schema_version"`
	RunID         string            `json:"run_id"`
	AttemptID     string            `json:"attempt_id"`
	SpecDigest    string            `json:"spec_digest,omitempty"`
	Sequence      uint64            `json:"sequence"`
	Type          RunEventType      `json:"type"`
	Stage         DrillStage        `json:"stage,omitempty"`
	Outcome       StageOutcome      `json:"outcome,omitempty"`
	Status        DrillStatus       `json:"status,omitempty"`
	OccurredAt    time.Time         `json:"occurred_at"`
	Message       string            `json:"message,omitempty"`
	Attributes    map[string]string `json:"attributes,omitempty"`
}

func (e RunEvent) Validate() error {
	if e.SchemaVersion != CurrentRunEventSchemaVersion {
		return fmt.Errorf("unsupported run event schema version %q", e.SchemaVersion)
	}
	if strings.TrimSpace(e.RunID) == "" {
		return fmt.Errorf("run event run_id is required")
	}
	if e.RunID != strings.TrimSpace(e.RunID) {
		return fmt.Errorf("run event run_id must not contain surrounding whitespace")
	}
	if strings.TrimSpace(e.AttemptID) == "" {
		return fmt.Errorf("run event attempt_id is required")
	}
	if e.AttemptID != strings.TrimSpace(e.AttemptID) {
		return fmt.Errorf("run event attempt_id must not contain surrounding whitespace")
	}
	if e.SpecDigest != "" && !IsSHA256Digest(e.SpecDigest) {
		return fmt.Errorf("run event spec_digest must be a sha256 digest")
	}
	if e.Sequence == 0 {
		return fmt.Errorf("run event sequence must be positive")
	}
	if !e.Type.IsKnown() {
		return fmt.Errorf("unsupported run event type %q", e.Type)
	}
	if e.OccurredAt.IsZero() {
		return fmt.Errorf("run event occurred_at is required")
	}
	for key := range e.Attributes {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("run event attribute key is required")
		}
	}

	switch e.Type {
	case RunEventStarted:
		if e.Stage != "" || e.Outcome != "" || e.Status != "" {
			return fmt.Errorf("run_started event cannot contain stage, outcome, or status")
		}
	case RunEventStageStarted:
		if !e.Stage.IsKnown() {
			return fmt.Errorf("stage_started event requires a known stage")
		}
		if e.Outcome != "" || e.Status != "" {
			return fmt.Errorf("stage_started event cannot contain outcome or status")
		}
	case RunEventStageCompleted:
		if !e.Stage.IsKnown() {
			return fmt.Errorf("stage_completed event requires a known stage")
		}
		if !e.Outcome.IsTerminal() {
			return fmt.Errorf("stage_completed event requires a terminal outcome")
		}
		if e.Status != "" {
			return fmt.Errorf("stage_completed event cannot contain run status")
		}
	case RunEventFinished:
		if e.Stage != "" || e.Outcome != "" {
			return fmt.Errorf("run_finished event cannot contain stage or outcome")
		}
		if !e.Status.IsTerminal() {
			return fmt.Errorf("run_finished event requires a terminal status")
		}
	}
	return nil
}
