package recoveryproof

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/jsonutil"
	"github.com/r314tive/pgdrill/internal/model"
)

const (
	CheckName                 = "recovery-target-attainment"
	EvidenceSource            = "postgres-recovery"
	ObservationSchema         = "pgdrill.recovery-observation/v2"
	ProofProtocolAttribute    = "proof_protocol"
	RecoveryStateAttribute    = "recovery_state"
	TargetTypeAttribute       = "recovery_target_type"
	TargetValueAttribute      = "recovery_target_value"
	TargetTimelineAttribute   = "recovery_target_timeline"
	TargetInclusiveAttribute  = "recovery_target_inclusive"
	ConfiguredActionAttribute = "configured_recovery_target_action"

	defaultBinary       = "psql"
	defaultTimeout      = time.Minute
	defaultPollInterval = 500 * time.Millisecond
)

const observationQuery = `WITH recovery_state AS MATERIALIZED (
  SELECT pg_is_in_recovery() AS in_recovery
)
SELECT json_build_object(
  'schema_version', 'pgdrill.recovery-observation/v2',
  'in_recovery', in_recovery,
  'replay_paused', CASE
    WHEN in_recovery THEN pg_is_wal_replay_paused()
    ELSE false
  END,
  'replay_pause_state', CASE
    WHEN in_recovery THEN pg_get_wal_replay_pause_state()
    ELSE 'not paused'
  END,
  'recovery_target', current_setting('recovery_target', true),
  'recovery_target_time', current_setting('recovery_target_time', true),
  'recovery_target_lsn', current_setting('recovery_target_lsn', true),
  'recovery_target_xid', current_setting('recovery_target_xid', true),
  'recovery_target_name', current_setting('recovery_target_name', true),
  'recovery_target_timeline', current_setting('recovery_target_timeline', true),
  'recovery_target_inclusive', current_setting('recovery_target_inclusive', true),
  'recovery_target_action', current_setting('recovery_target_action', true)
)::text
FROM recovery_state;`

type Config struct {
	Binary       string
	Env          map[string]string
	Timeout      time.Duration
	PollInterval time.Duration
	RedactValues []string
}

type Verifier struct {
	cfg    Config
	runner command.Runner
}

type Observation struct {
	SchemaVersion           string `json:"schema_version"`
	InRecovery              bool   `json:"in_recovery"`
	ReplayPaused            bool   `json:"replay_paused"`
	ReplayPauseState        string `json:"replay_pause_state"`
	RecoveryTarget          string `json:"recovery_target"`
	RecoveryTargetTime      string `json:"recovery_target_time"`
	RecoveryTargetLSN       string `json:"recovery_target_lsn"`
	RecoveryTargetXID       string `json:"recovery_target_xid"`
	RecoveryTargetName      string `json:"recovery_target_name"`
	RecoveryTargetTimeline  string `json:"recovery_target_timeline"`
	RecoveryTargetInclusive string `json:"recovery_target_inclusive"`
	RecoveryTargetAction    string `json:"recovery_target_action"`
}

func New(cfg Config, runner command.Runner) *Verifier {
	if runner == nil {
		runner = command.NewRunner(command.Options{DefaultTimeout: effectiveTimeout(cfg.Timeout)})
	}
	return &Verifier{cfg: cfg, runner: runner}
}

func (v *Verifier) VerifyRecoveryTarget(
	ctx context.Context,
	pg model.RunningPostgres,
	target model.RecoveryTarget,
) (model.CheckReport, error) {
	if v == nil || v.runner == nil {
		return failedReport("recovery target verifier is required", nil), nil
	}
	target = target.Normalized()
	if err := target.Validate(); err != nil {
		return failedReport("invalid recovery target", nil), nil
	}
	if strings.TrimSpace(pg.ConnString) == "" {
		return failedReport("running postgres conn_string is required", nil), nil
	}

	timeout := effectiveTimeout(v.cfg.Timeout)
	proofCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastEvidence *model.EvidenceRecord
	var lastTransientErr error
	for {
		if report, done := contextFailureReport(proofCtx, lastEvidence, lastTransientErr); done {
			return report, nil
		}

		result, runErr := v.runner.Run(proofCtx, command.Invocation{
			Path:         v.binary(),
			Args:         []string{"-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-d", pg.ConnString, "-c", observationQuery},
			Env:          copyStringMap(v.cfg.Env),
			Timeout:      timeout,
			RedactValues: append(append([]string{}, v.cfg.RedactValues...), pg.ConnString),
		})
		evidence := commandEvidence(result.Evidence)
		lastEvidence = &evidence
		if runErr != nil {
			message := boundedMessage("execute recovery target observation: ", result.RedactError(runErr))
			return failedReport(message, &evidence), nil
		}
		if !result.Evidence.ExitStatus.Success {
			message := "recovery target observation failed: " + result.Evidence.ExitStatus.Summary()
			if detail := strings.TrimSpace(result.Evidence.Stderr); detail != "" {
				message += ": " + detail
			}
			return failedReport(boundedMessage("", fmt.Errorf("%s", message)), &evidence), nil
		}
		if result.Evidence.StdoutTruncated {
			return failedReport(
				"recovery target observation stdout was truncated",
				&evidence,
			), nil
		}

		observation, err := ParseObservation([]byte(result.Evidence.Stdout))
		if err != nil {
			return failedReport(
				boundedMessage("parse recovery target observation: ", result.RedactError(err)),
				&evidence,
			), nil
		}
		state, transient, err := evaluateObservation(target, observation)
		if err == nil {
			check := model.Check{
				Name:        CheckName,
				Status:      model.CheckStatusPassed,
				Message:     "PostgreSQL runtime settings and recovery state prove target attainment",
				EvidenceIDs: []string{evidence.ID},
				Attributes:  proofAttributes(target, observation, state),
			}
			return model.CheckReport{
				Checks:   []model.Check{check},
				Evidence: []model.EvidenceRecord{evidence},
			}, nil
		}
		if !transient {
			return failedReport(
				boundedMessage("recovery target is not proven: ", result.RedactError(err)),
				&evidence,
			), nil
		}
		lastTransientErr = result.RedactError(err)
		if waitErr := waitForNextObservation(proofCtx, effectivePollInterval(v.cfg.PollInterval)); waitErr != nil {
			return failedReport(
				boundedMessage("recovery target was not proven before observation deadline: ", lastTransientErr),
				&evidence,
			), nil
		}
	}
}

func contextFailureReport(
	ctx context.Context,
	lastEvidence *model.EvidenceRecord,
	lastTransientErr error,
) (model.CheckReport, bool) {
	err := ctx.Err()
	if err == nil {
		return model.CheckReport{}, false
	}
	if lastEvidence != nil {
		return failedReport(
			boundedMessage(
				"recovery target was not proven before observation deadline: ",
				lastTransientErr,
			),
			lastEvidence,
		), true
	}
	return failedReport(
		boundedMessage("recovery target observation ended: ", err),
		nil,
	), true
}

func ParseObservation(data []byte) (Observation, error) {
	var observation Observation
	if err := jsonutil.DecodeOneStrict(data, &observation); err != nil {
		return Observation{}, err
	}
	if observation.SchemaVersion != ObservationSchema {
		return Observation{}, fmt.Errorf(
			"unsupported observation schema %q",
			observation.SchemaVersion,
		)
	}
	return observation, nil
}

func Evaluate(target model.RecoveryTarget, observation Observation) (string, error) {
	state, _, err := evaluateObservation(target, observation)
	return state, err
}

func evaluateObservation(
	target model.RecoveryTarget,
	observation Observation,
) (state string, transient bool, err error) {
	target = target.Normalized()
	if err := target.Validate(); err != nil {
		return "", false, err
	}
	if observation.SchemaVersion != ObservationSchema {
		return "", false, fmt.Errorf("unsupported observation schema %q", observation.SchemaVersion)
	}
	if err := validateConfiguredTarget(target, observation); err != nil {
		return "", false, err
	}

	action := observation.RecoveryTargetAction
	pauseState := observation.ReplayPauseState
	if action == "promote" {
		return "", false, fmt.Errorf(
			"recovery_target_action promote cannot prove target attainment",
		)
	}
	switch action {
	case "pause", "shutdown":
	default:
		return "", false, fmt.Errorf(
			"unsupported recovery_target_action %q",
			action,
		)
	}
	if target.Type == model.RecoveryTargetLatest {
		if observation.InRecovery {
			return "", true, fmt.Errorf("latest recovery is still in progress")
		}
		if observation.ReplayPaused {
			return "", false, fmt.Errorf("latest recovery reports a WAL replay pause request after recovery")
		}
		if pauseState != "not paused" {
			return "", false, fmt.Errorf(
				"latest recovery reports replay pause state %q after recovery",
				observation.ReplayPauseState,
			)
		}
		return "recovery_complete", false, nil
	}

	if action != "pause" {
		return "", false, fmt.Errorf(
			"targeted recovery requires recovery_target_action pause, got %q",
			action,
		)
	}
	if !observation.InRecovery {
		return "", false, fmt.Errorf("targeted recovery left recovery before a paused target was proven")
	}

	switch pauseState {
	case "paused":
		if !observation.ReplayPaused {
			return "", false, fmt.Errorf(
				"replay pause state is paused but the pause request flag is false",
			)
		}
		return "paused_at_target", false, nil
	case "not paused", "pause requested":
		return "", true, fmt.Errorf(
			"targeted recovery has not reached an actual paused replay state",
		)
	default:
		return "", false, fmt.Errorf(
			"unsupported WAL replay pause state %q",
			observation.ReplayPauseState,
		)
	}
}

func ValidateReport(target model.RecoveryTarget, report model.CheckReport) error {
	if len(report.Checks) != 1 {
		return fmt.Errorf("recovery target proof must contain exactly one check")
	}
	check := report.Checks[0]
	if check.Name != CheckName {
		return fmt.Errorf("recovery target proof check name is %q", check.Name)
	}
	if err := check.Validate(); err != nil {
		return err
	}
	if check.Status != model.CheckStatusPassed {
		return fmt.Errorf("recovery target proof check did not pass")
	}
	if len(check.EvidenceIDs) != 1 {
		return fmt.Errorf("passed recovery target proof requires exactly one evidence reference")
	}
	if len(report.Evidence) != 1 || report.Evidence[0].ID != check.EvidenceIDs[0] {
		return fmt.Errorf("recovery target proof evidence does not match its check reference")
	}
	evidence := report.Evidence[0]
	if err := evidence.Validate(); err != nil {
		return err
	}
	if evidence.Kind != model.EvidenceCommand ||
		evidence.Source != EvidenceSource ||
		evidence.Command == nil ||
		!evidence.Command.ExitStatus.Success ||
		evidence.Command.StdoutTruncated {
		return fmt.Errorf("recovery target proof requires complete successful command evidence")
	}
	if evidence.Attributes["operation"] != "observe-recovery-target" {
		return fmt.Errorf("recovery target proof evidence operation is invalid")
	}
	observation, err := ParseObservation([]byte(evidence.Command.Stdout))
	if err != nil {
		return fmt.Errorf("parse retained recovery target observation: %w", err)
	}
	state, err := Evaluate(target, observation)
	if err != nil {
		return fmt.Errorf("validate retained recovery target observation: %w", err)
	}
	expectedAttributes := proofAttributes(target.Normalized(), observation, state)
	if !equalStringMap(check.Attributes, expectedAttributes) {
		return fmt.Errorf("recovery target proof attributes do not match retained observation")
	}
	return nil
}

func ValidatePersisted(
	target model.RecoveryTarget,
	checks []model.Check,
	evidence []model.EvidenceRecord,
) error {
	var proofChecks []model.Check
	for _, check := range checks {
		if check.Name == CheckName {
			proofChecks = append(proofChecks, check)
		}
	}
	if len(proofChecks) != 1 {
		return fmt.Errorf("passed report requires exactly one recovery target proof check")
	}
	referenced := make(map[string]struct{}, len(proofChecks[0].EvidenceIDs))
	for _, id := range proofChecks[0].EvidenceIDs {
		referenced[id] = struct{}{}
	}
	proofEvidence := make([]model.EvidenceRecord, 0, len(referenced))
	for _, record := range evidence {
		if _, ok := referenced[record.ID]; ok {
			proofEvidence = append(proofEvidence, record)
		}
	}
	return ValidateReport(target, model.CheckReport{
		Checks:   proofChecks,
		Evidence: proofEvidence,
	})
}

func validateConfiguredTarget(target model.RecoveryTarget, observation Observation) error {
	configured := map[model.RecoveryTargetType]string{
		model.RecoveryTargetImmediate:    strings.TrimSpace(observation.RecoveryTarget),
		model.RecoveryTargetTimestamp:    strings.TrimSpace(observation.RecoveryTargetTime),
		model.RecoveryTargetLSN:          strings.TrimSpace(observation.RecoveryTargetLSN),
		model.RecoveryTargetXID:          strings.TrimSpace(observation.RecoveryTargetXID),
		model.RecoveryTargetRestorePoint: strings.TrimSpace(observation.RecoveryTargetName),
	}
	for targetType, value := range configured {
		if target.Type == model.RecoveryTargetLatest || targetType != target.Type {
			if value != "" {
				return fmt.Errorf("unexpected configured %s recovery target", targetType)
			}
			continue
		}
		if value == "" {
			return fmt.Errorf("configured %s recovery target is empty", targetType)
		}
		if err := compareTargetValue(target, value); err != nil {
			return err
		}
	}

	expectedTimeline := target.Timeline
	if expectedTimeline == "" {
		expectedTimeline = "latest"
	}
	if !sameTimeline(expectedTimeline, observation.RecoveryTargetTimeline) {
		return fmt.Errorf(
			"configured recovery target timeline %q does not match requested timeline",
			observation.RecoveryTargetTimeline,
		)
	}
	if target.Type == model.RecoveryTargetTimestamp ||
		target.Type == model.RecoveryTargetLSN ||
		target.Type == model.RecoveryTargetXID {
		configuredInclusive, err := parsePostgresBool(observation.RecoveryTargetInclusive)
		if err != nil {
			return fmt.Errorf("configured recovery_target_inclusive: %w", err)
		}
		expectedInclusive := true
		if target.Inclusive != nil {
			expectedInclusive = *target.Inclusive
		}
		if configuredInclusive != expectedInclusive {
			return fmt.Errorf("configured recovery_target_inclusive does not match requested value")
		}
	}
	return nil
}

func compareTargetValue(target model.RecoveryTarget, configured string) error {
	switch target.Type {
	case model.RecoveryTargetImmediate:
		if !strings.EqualFold(configured, "immediate") {
			return fmt.Errorf("configured immediate recovery target does not match requested value")
		}
	case model.RecoveryTargetTimestamp:
		expected, err := target.Timestamp()
		if err != nil {
			return err
		}
		actual, err := parsePostgresTimestamp(configured)
		if err != nil || !actual.Equal(expected) {
			return fmt.Errorf("configured timestamp recovery target does not match requested value")
		}
	case model.RecoveryTargetLSN:
		expected, err := parseLSN(target.Value)
		if err != nil {
			return err
		}
		actual, err := parseLSN(configured)
		if err != nil || actual != expected {
			return fmt.Errorf("configured LSN recovery target does not match requested value")
		}
	case model.RecoveryTargetXID:
		expected, err := strconv.ParseUint(target.Value, 10, 32)
		if err != nil {
			return err
		}
		actual, err := strconv.ParseUint(configured, 10, 32)
		if err != nil || actual != expected {
			return fmt.Errorf("configured XID recovery target does not match requested value")
		}
	case model.RecoveryTargetRestorePoint:
		if configured != target.Value {
			return fmt.Errorf("configured restore point recovery target does not match requested value")
		}
	default:
		return fmt.Errorf("unsupported recovery target %q", target.Type)
	}
	return nil
}

func proofAttributes(
	target model.RecoveryTarget,
	observation Observation,
	state string,
) map[string]string {
	inclusive := "default"
	if target.Inclusive != nil {
		inclusive = strconv.FormatBool(*target.Inclusive)
	}
	return map[string]string{
		ProofProtocolAttribute:    ObservationSchema,
		RecoveryStateAttribute:    state,
		TargetTypeAttribute:       string(target.Type),
		TargetValueAttribute:      target.Value,
		TargetTimelineAttribute:   target.Timeline,
		TargetInclusiveAttribute:  inclusive,
		ConfiguredActionAttribute: observation.RecoveryTargetAction,
	}
}

func failedReport(message string, evidence *model.EvidenceRecord) model.CheckReport {
	report := model.CheckReport{Checks: []model.Check{{
		Name:    CheckName,
		Status:  model.CheckStatusFailed,
		Message: message,
	}}}
	if evidence != nil {
		report.Checks[0].EvidenceIDs = []string{evidence.ID}
		report.Evidence = []model.EvidenceRecord{*evidence}
	}
	return report
}

func commandEvidence(evidence model.CommandEvidence) model.EvidenceRecord {
	collectedAt := evidence.FinishedAt
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}
	return model.EvidenceRecord{
		ID:          "recovery-target:observe:" + collectedAt.Format(time.RFC3339Nano),
		Kind:        model.EvidenceCommand,
		Source:      EvidenceSource,
		CollectedAt: collectedAt,
		Command:     &evidence,
		Attributes: map[string]string{
			"operation": "observe-recovery-target",
		},
	}
}

func parsePostgresTimestamp(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999Z07",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05Z07",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp format")
}

func parseLSN(value string) (uint64, error) {
	high, low, ok := strings.Cut(strings.TrimSpace(value), "/")
	if !ok || high == "" || low == "" || strings.Contains(low, "/") {
		return 0, fmt.Errorf("invalid LSN")
	}
	highValue, err := strconv.ParseUint(high, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN")
	}
	lowValue, err := strconv.ParseUint(low, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN")
	}
	return highValue<<32 | lowValue, nil
}

func parsePostgresBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean value")
	}
}

func sameTimeline(expected, actual string) bool {
	expected = strings.ToLower(strings.TrimSpace(expected))
	actual = strings.ToLower(strings.TrimSpace(actual))
	if expected == actual {
		return true
	}
	expectedValue, expectedErr := strconv.ParseUint(expected, 10, 32)
	actualValue, actualErr := strconv.ParseUint(actual, 10, 32)
	return expectedErr == nil && actualErr == nil && expectedValue == actualValue
}

func boundedMessage(prefix string, err error) string {
	message := prefix
	if err != nil {
		message += err.Error()
	}
	message = strings.ToValidUTF8(message, "\uFFFD")
	if len(message) <= model.MaxCheckMessageBytes {
		return message
	}
	message = message[:model.MaxCheckMessageBytes]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}

func effectiveTimeout(value time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return defaultTimeout
}

func effectivePollInterval(value time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return defaultPollInterval
}

func waitForNextObservation(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (v *Verifier) binary() string {
	if v.cfg.Binary != "" {
		return v.cfg.Binary
	}
	return defaultBinary
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}
