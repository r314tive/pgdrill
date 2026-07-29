package adapterutil

import (
	"maps"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func CommandEvidence(
	provider model.ProviderType,
	operation string,
	evidence model.CommandEvidence,
) model.EvidenceRecord {
	collectedAt := evidence.FinishedAt
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}
	return model.EvidenceRecord{
		ID:          string(provider) + ":" + operation + ":" + collectedAt.Format(time.RFC3339Nano),
		Kind:        model.EvidenceCommand,
		Source:      string(provider),
		CollectedAt: collectedAt,
		Command:     &evidence,
		Attributes: map[string]string{
			"operation": operation,
		},
	}
}

func PlanEvidence(provider model.ProviderType, operation string) model.EvidenceRecord {
	now := time.Now().UTC()
	return model.EvidenceRecord{
		ID:          string(provider) + ":" + operation + ":" + now.Format(time.RFC3339Nano),
		Kind:        model.EvidencePlan,
		Source:      string(provider),
		CollectedAt: now,
		Attributes: map[string]string{
			"operation": operation,
		},
	}
}

func DurationString(value time.Duration) string {
	if value == 0 {
		return ""
	}
	return value.String()
}

func CloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	return maps.Clone(values)
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func StringMapOrNil(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	return values
}
