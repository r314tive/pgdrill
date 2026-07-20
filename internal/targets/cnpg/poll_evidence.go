package cnpg

import (
	"reflect"
	"strconv"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

const (
	pollObservationsAttribute  = "poll_observations"
	pollFirstObservedAttribute = "poll_first_observed_at"
	pollLastObservedAttribute  = "poll_last_observed_at"
)

// pollEvidence retains each observed state while compacting adjacent,
// semantically identical snapshots from the same polling operation.
type pollEvidence struct {
	records         []model.EvidenceRecord
	lastByOperation map[string]int
}

func newPollEvidence() *pollEvidence {
	return &pollEvidence{lastByOperation: map[string]int{}}
}

func (p *pollEvidence) Add(records ...model.EvidenceRecord) {
	for _, record := range records {
		operation := record.Attributes["operation"]
		if operation == "" {
			p.records = append(p.records, record)
			continue
		}
		if index, ok := p.lastByOperation[operation]; ok && samePollObservation(p.records[index], record) {
			compactPollObservation(&p.records[index], record)
			continue
		}
		p.records = append(p.records, record)
		p.lastByOperation[operation] = len(p.records) - 1
	}
}

func (p *pollEvidence) Records() []model.EvidenceRecord {
	return append([]model.EvidenceRecord{}, p.records...)
}

func samePollObservation(left, right model.EvidenceRecord) bool {
	if left.Kind != right.Kind || left.Source != right.Source || left.Command == nil || right.Command == nil {
		return false
	}
	leftCommand := *left.Command
	rightCommand := *right.Command
	leftCommand.StartedAt = time.Time{}
	leftCommand.FinishedAt = time.Time{}
	leftCommand.DurationMillis = 0
	rightCommand.StartedAt = time.Time{}
	rightCommand.FinishedAt = time.Time{}
	rightCommand.DurationMillis = 0
	return reflect.DeepEqual(leftCommand, rightCommand)
}

func compactPollObservation(retained *model.EvidenceRecord, latest model.EvidenceRecord) {
	if retained.Attributes == nil {
		retained.Attributes = map[string]string{}
	}
	count := 1
	if value := retained.Attributes[pollObservationsAttribute]; value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			count = parsed
		}
	}
	retained.Attributes[pollObservationsAttribute] = strconv.Itoa(count + 1)
	if retained.Attributes[pollFirstObservedAttribute] == "" {
		retained.Attributes[pollFirstObservedAttribute] = retained.CollectedAt.UTC().Format(time.RFC3339Nano)
	}
	retained.Attributes[pollLastObservedAttribute] = latest.CollectedAt.UTC().Format(time.RFC3339Nano)
}
