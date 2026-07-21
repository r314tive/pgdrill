package report

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

type metricLabel struct {
	name  string
	value string
}

func WritePrometheus(writer io.Writer, result model.DrillResult) error {
	if err := normalizeSchemaVersion(&result); err != nil {
		return err
	}
	baseLabels := []metricLabel{
		{name: "cluster", value: labelOrUnknown(result.Cluster)},
		{name: "provider", value: providerLabel(result.Provider)},
		{name: "target_type", value: targetTypeLabel(result.Target.Type)},
		{name: "recovery_target", value: recoveryTargetLabel(result.RecoveryTarget.Type)},
	}
	if _, err := fmt.Fprintln(writer, "# HELP pgdrill_report_info Report format information."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "# TYPE pgdrill_report_info gauge"); err != nil {
		return err
	}
	if err := writeMetric(writer, "pgdrill_report_info", []metricLabel{
		{name: "cluster", value: labelOrUnknown(result.Cluster)},
		{name: "schema_version", value: result.SchemaVersion},
	}, "1"); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(writer, "# HELP pgdrill_drill_status Last drill status as a one-hot gauge."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "# TYPE pgdrill_drill_status gauge"); err != nil {
		return err
	}
	for _, status := range []model.DrillStatus{
		model.DrillStatusPassed,
		model.DrillStatusFailed,
		model.DrillStatusAborted,
		model.DrillStatusUnknown,
	} {
		value := "0"
		if normalizeDrillStatus(result.Status) == status {
			value = "1"
		}
		labels := appendMetricLabel(baseLabels, "status", string(status))
		if err := writeMetric(writer, "pgdrill_drill_status", labels, value); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(writer, "# HELP pgdrill_failure_info Failure lifecycle stage for the last drill."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "# TYPE pgdrill_failure_info gauge"); err != nil {
		return err
	}
	failureLabels := appendMetricLabel(baseLabels, "stage", failureStage(result))
	if err := writeMetric(writer, "pgdrill_failure_info", failureLabels, "1"); err != nil {
		return err
	}

	durationLabels := appendMetricLabel(baseLabels, "status", string(normalizeDrillStatus(result.Status)))
	if _, err := fmt.Fprintln(writer, "# HELP pgdrill_drill_duration_seconds Last drill duration in seconds."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "# TYPE pgdrill_drill_duration_seconds gauge"); err != nil {
		return err
	}
	if err := writeMetric(writer, "pgdrill_drill_duration_seconds", durationLabels, formatFloat(durationSeconds(result.StartedAt, result.FinishedAt))); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(writer, "# HELP pgdrill_drill_started_timestamp_seconds Last drill start time as a Unix timestamp."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "# TYPE pgdrill_drill_started_timestamp_seconds gauge"); err != nil {
		return err
	}
	if err := writeMetric(writer, "pgdrill_drill_started_timestamp_seconds", durationLabels, timestampSeconds(result.StartedAt)); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(writer, "# HELP pgdrill_drill_finished_timestamp_seconds Last drill finish time as a Unix timestamp."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "# TYPE pgdrill_drill_finished_timestamp_seconds gauge"); err != nil {
		return err
	}
	if err := writeMetric(writer, "pgdrill_drill_finished_timestamp_seconds", durationLabels, timestampSeconds(result.FinishedAt)); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(writer, "# HELP pgdrill_checks_total Number of checks in the last drill grouped by check name, probe, and status."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "# TYPE pgdrill_checks_total gauge"); err != nil {
		return err
	}
	for _, sample := range checkCountSamples(result.Cluster, result.Provider, result.Checks) {
		if err := writeMetric(writer, "pgdrill_checks_total", sample.labels, strconv.Itoa(sample.value)); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintln(writer, "# HELP pgdrill_evidence_records_total Number of evidence records in the last drill grouped by kind."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "# TYPE pgdrill_evidence_records_total gauge"); err != nil {
		return err
	}
	for _, sample := range evidenceCountSamples(result.Cluster, result.Provider, result.Evidence) {
		if err := writeMetric(writer, "pgdrill_evidence_records_total", sample.labels, strconv.Itoa(sample.value)); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintln(writer, "# HELP pgdrill_operations_total Number of mutation operations in the last drill grouped by kind, state, and reconciliation status."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "# TYPE pgdrill_operations_total gauge"); err != nil {
		return err
	}
	for _, sample := range operationCountSamples(result.Cluster, result.Provider, result.Operations) {
		if err := writeMetric(writer, "pgdrill_operations_total", sample.labels, strconv.Itoa(sample.value)); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintln(writer, "# HELP pgdrill_artifacts_total Number of referenced artifacts in the last drill grouped by retention and redaction classification."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "# TYPE pgdrill_artifacts_total gauge"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "# HELP pgdrill_artifact_bytes Total referenced artifact bytes in the last drill grouped by retention and redaction classification."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "# TYPE pgdrill_artifact_bytes gauge"); err != nil {
		return err
	}
	for _, sample := range artifactSamples(result.Cluster, result.Provider, result.Artifacts) {
		if err := writeMetric(writer, "pgdrill_artifacts_total", sample.labels, strconv.Itoa(sample.count)); err != nil {
			return err
		}
		if err := writeMetric(writer, "pgdrill_artifact_bytes", sample.labels, strconv.FormatInt(sample.bytes, 10)); err != nil {
			return err
		}
	}

	return nil
}

func failureStage(result model.DrillResult) string {
	if result.Failure != nil {
		if result.Failure.Stage.IsKnown() {
			return string(result.Failure.Stage)
		}
		return "unknown"
	}
	switch normalizeDrillStatus(result.Status) {
	case model.DrillStatusFailed, model.DrillStatusAborted:
		return "unknown"
	default:
		return "none"
	}
}

type metricSample struct {
	labels []metricLabel
	value  int
}

func checkCountSamples(cluster string, provider model.ProviderType, checks []model.Check) []metricSample {
	counts := map[string]int{}
	labelsByKey := map[string][]metricLabel{}
	for _, check := range checks {
		labels := []metricLabel{
			{name: "cluster", value: labelOrUnknown(cluster)},
			{name: "provider", value: providerLabel(provider)},
			{name: "check", value: labelOrUnknown(check.Name)},
			{name: "probe", value: probeLabel(check.Probe)},
			{name: "status", value: checkStatusLabel(check.Status)},
		}
		key := metricKey(labels)
		counts[key]++
		labelsByKey[key] = labels
	}
	return samplesFromCounts(counts, labelsByKey)
}

func evidenceCountSamples(cluster string, provider model.ProviderType, records []model.EvidenceRecord) []metricSample {
	counts := map[string]int{}
	labelsByKey := map[string][]metricLabel{}
	for _, record := range records {
		labels := []metricLabel{
			{name: "cluster", value: labelOrUnknown(cluster)},
			{name: "provider", value: providerLabel(provider)},
			{name: "kind", value: evidenceKindLabel(record.Kind)},
		}
		key := metricKey(labels)
		counts[key]++
		labelsByKey[key] = labels
	}
	return samplesFromCounts(counts, labelsByKey)
}

func operationCountSamples(cluster string, provider model.ProviderType, checkpoints []model.OperationCheckpoint) []metricSample {
	counts := map[string]int{}
	labelsByKey := map[string][]metricLabel{}
	for _, checkpoint := range checkpoints {
		labels := []metricLabel{
			{name: "cluster", value: labelOrUnknown(cluster)},
			{name: "provider", value: providerLabel(provider)},
			{name: "kind", value: operationKindLabel(checkpoint.Operation.Kind)},
			{name: "state", value: operationStateLabel(checkpoint.State)},
			{name: "reconciled", value: strconv.FormatBool(checkpoint.Reconciled)},
		}
		key := metricKey(labels)
		counts[key]++
		labelsByKey[key] = labels
	}
	return samplesFromCounts(counts, labelsByKey)
}

type artifactMetricSample struct {
	labels []metricLabel
	count  int
	bytes  int64
}

func artifactSamples(cluster string, provider model.ProviderType, artifacts []model.ArtifactRef) []artifactMetricSample {
	counts := map[string]int{}
	bytes := map[string]int64{}
	labelsByKey := map[string][]metricLabel{}
	for _, artifact := range artifacts {
		labels := []metricLabel{
			{name: "cluster", value: labelOrUnknown(cluster)},
			{name: "provider", value: providerLabel(provider)},
			{name: "retention", value: artifactRetentionLabel(artifact.RetentionClass)},
			{name: "redaction", value: artifactRedactionLabel(artifact.RedactionState)},
		}
		key := metricKey(labels)
		counts[key]++
		if artifact.SizeBytes > 0 {
			bytes[key] += artifact.SizeBytes
		}
		labelsByKey[key] = labels
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	samples := make([]artifactMetricSample, 0, len(keys))
	for _, key := range keys {
		samples = append(samples, artifactMetricSample{
			labels: labelsByKey[key],
			count:  counts[key],
			bytes:  bytes[key],
		})
	}
	return samples
}

func samplesFromCounts(counts map[string]int, labelsByKey map[string][]metricLabel) []metricSample {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	samples := make([]metricSample, 0, len(keys))
	for _, key := range keys {
		samples = append(samples, metricSample{
			labels: labelsByKey[key],
			value:  counts[key],
		})
	}
	return samples
}

func writeMetric(writer io.Writer, name string, labels []metricLabel, value string) error {
	if _, err := fmt.Fprintf(writer, "%s%s %s\n", name, formatLabels(labels), value); err != nil {
		return err
	}
	return nil
}

func formatLabels(labels []metricLabel) string {
	if len(labels) == 0 {
		return ""
	}
	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		parts = append(parts, label.name+`="`+escapeLabelValue(label.value)+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func appendMetricLabel(labels []metricLabel, name string, value string) []metricLabel {
	copied := make([]metricLabel, 0, len(labels)+1)
	copied = append(copied, labels...)
	copied = append(copied, metricLabel{name: name, value: value})
	return copied
}

func metricKey(labels []metricLabel) string {
	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		parts = append(parts, label.name+"="+label.value)
	}
	return strings.Join(parts, "\xff")
}

func escapeLabelValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func labelOrUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func providerLabel(value model.ProviderType) string {
	if !value.IsKnown() {
		return "unknown"
	}
	return string(value)
}

func targetTypeLabel(value model.RestoreTargetType) string {
	if !value.IsKnown() {
		return "unknown"
	}
	return string(value)
}

func recoveryTargetLabel(value model.RecoveryTargetType) string {
	if !value.IsKnown() {
		return "unknown"
	}
	return string(value)
}

func probeLabel(value model.ProbeType) string {
	if !value.IsKnown() {
		return "unknown"
	}
	return string(value)
}

func checkStatusLabel(value model.CheckStatus) string {
	if !value.IsTerminal() {
		return "unknown"
	}
	return string(value)
}

func evidenceKindLabel(value model.EvidenceKind) string {
	if !value.IsKnown() {
		return "unknown"
	}
	return string(value)
}

func operationKindLabel(value model.OperationKind) string {
	if !value.IsKnown() {
		return "unknown"
	}
	return string(value)
}

func operationStateLabel(value model.OperationState) string {
	if !value.IsKnown() {
		return "unknown"
	}
	return string(value)
}

func artifactRetentionLabel(value model.ArtifactRetentionClass) string {
	if !value.IsKnown() {
		return "unknown"
	}
	return string(value)
}

func artifactRedactionLabel(value model.ArtifactRedactionState) string {
	if !value.IsKnown() {
		return "unknown"
	}
	return string(value)
}

func normalizeDrillStatus(status model.DrillStatus) model.DrillStatus {
	switch status {
	case model.DrillStatusPassed, model.DrillStatusFailed, model.DrillStatusAborted:
		return status
	default:
		return model.DrillStatusUnknown
	}
}

func durationSeconds(started time.Time, finished time.Time) float64 {
	if started.IsZero() || finished.IsZero() || finished.Before(started) {
		return 0
	}
	return finished.Sub(started).Seconds()
}

func timestampSeconds(value time.Time) string {
	if value.IsZero() {
		return "0"
	}
	return strconv.FormatInt(value.UTC().Unix(), 10)
}

func formatFloat(value float64) string {
	if value == float64(int64(value)) {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'f', 3, 64)
}
