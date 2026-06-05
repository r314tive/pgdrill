package model

type ProviderType string

const (
	ProviderWALG       ProviderType = "wal-g"
	ProviderBarman     ProviderType = "barman"
	ProviderPGBackRest ProviderType = "pgbackrest"
)

type RestoreTargetType string

const (
	RestoreTargetLocal      RestoreTargetType = "local"
	RestoreTargetContainer  RestoreTargetType = "container"
	RestoreTargetKubernetes RestoreTargetType = "kubernetes"
)

type RecoveryTargetType string

const (
	RecoveryTargetImmediate    RecoveryTargetType = "immediate"
	RecoveryTargetLatest       RecoveryTargetType = "latest"
	RecoveryTargetTimestamp    RecoveryTargetType = "timestamp"
	RecoveryTargetLSN          RecoveryTargetType = "lsn"
	RecoveryTargetXID          RecoveryTargetType = "xid"
	RecoveryTargetRestorePoint RecoveryTargetType = "restore_point"
)

type ProbeType string

const (
	ProbePGIsReady ProbeType = "pg_isready"
	ProbeSQL       ProbeType = "sql"
	ProbeAMCheck   ProbeType = "amcheck"
	ProbePGDump    ProbeType = "pg_dump"
)

type Overview struct {
	Providers       []ProviderType       `json:"providers"`
	RestoreTargets  []RestoreTargetType  `json:"restore_targets"`
	RecoveryTargets []RecoveryTargetType `json:"recovery_targets"`
	Probes          []ProbeType          `json:"probes"`
}

func ProjectOverview() Overview {
	return Overview{
		Providers: []ProviderType{
			ProviderWALG,
			ProviderBarman,
			ProviderPGBackRest,
		},
		RestoreTargets: []RestoreTargetType{
			RestoreTargetLocal,
			RestoreTargetContainer,
			RestoreTargetKubernetes,
		},
		RecoveryTargets: []RecoveryTargetType{
			RecoveryTargetImmediate,
			RecoveryTargetLatest,
			RecoveryTargetTimestamp,
			RecoveryTargetLSN,
			RecoveryTargetXID,
			RecoveryTargetRestorePoint,
		},
		Probes: []ProbeType{
			ProbePGIsReady,
			ProbeSQL,
			ProbeAMCheck,
			ProbePGDump,
		},
	}
}
