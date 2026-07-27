package model

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const CurrentRunEventSchemaVersion = "pgdrill.run-event/v1alpha1"

const (
	MaxRunEventMessageBytes   = 4 << 10
	MaxRunEventAttributes     = 32
	maxRunEventAttributeKey   = 128
	maxRunEventAttributeValue = 4 << 10
)

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
	if err := ValidateIdentity("run event run_id", e.RunID); err != nil {
		return err
	}
	if err := ValidateIdentity("run event attempt_id", e.AttemptID); err != nil {
		return err
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
	if !utf8.ValidString(e.Message) {
		return fmt.Errorf("run event message must be valid UTF-8")
	}
	if len(e.Message) > MaxRunEventMessageBytes {
		return fmt.Errorf("run event message exceeds %d bytes", MaxRunEventMessageBytes)
	}
	if len(e.Attributes) > MaxRunEventAttributes {
		return fmt.Errorf("run event attributes exceed maximum count %d", MaxRunEventAttributes)
	}
	for key, value := range e.Attributes {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("run event attribute key is required")
		}
		if key != strings.TrimSpace(key) {
			return fmt.Errorf("run event attribute key must not contain surrounding whitespace")
		}
		if len(key) > maxRunEventAttributeKey || !utf8.ValidString(key) {
			return fmt.Errorf("run event attribute key must be bounded valid UTF-8")
		}
		if strings.IndexFunc(key, unicode.IsControl) >= 0 {
			return fmt.Errorf("run event attribute key must not contain control characters")
		}
		if len(value) > maxRunEventAttributeValue || !utf8.ValidString(value) {
			return fmt.Errorf("run event attribute %q value must be bounded valid UTF-8", key)
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
