package adapters

import (
	"fmt"

	"github.com/r314tive/pgdrill/internal/adapters/barman"
	"github.com/r314tive/pgdrill/internal/adapters/pgbackrest"
	"github.com/r314tive/pgdrill/internal/adapters/pgprobackup"
	"github.com/r314tive/pgdrill/internal/adapters/walg"
	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/core"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/restorechecks/pgverifybackup"
)

func NewProvider(cfg config.ProviderConfig, restoreCfgs ...config.RestoreConfig) (core.BackupProvider, error) {
	restoreCfg := config.RestoreConfig{}
	if len(restoreCfgs) > 0 {
		restoreCfg = restoreCfgs[0]
	}
	verifyBackup := verifyBackupConfig(restoreCfg.VerifyBackup, cfg.RedactValues)

	switch cfg.Type {
	case model.ProviderWALG:
		return walg.New(walg.Config{
			Binary:         cfg.Binary,
			Env:            cfg.Env,
			WorkDir:        cfg.WorkDir,
			Timeout:        cfg.Timeout.Duration,
			RestoreTimeout: restoreCfg.Timeout.Duration,
			RedactValues:   cfg.RedactValues,
			WALVerify:      walVerifyConfig(cfg.WALVerify),
			VerifyBackup:   verifyBackup,
		}, nil), nil
	case model.ProviderBarman:
		return barman.New(barman.Config{
			Binary:         cfg.Binary,
			ConfigPath:     cfg.ConfigPath,
			Server:         cfg.Server,
			Env:            cfg.Env,
			WorkDir:        cfg.WorkDir,
			Timeout:        cfg.Timeout.Duration,
			RestoreTimeout: restoreCfg.Timeout.Duration,
			RedactValues:   cfg.RedactValues,
			Manifest:       barmanManifestConfig(cfg.BarmanManifest),
			BarmanVerify:   barmanVerifyConfig(cfg.BarmanVerify),
			VerifyBackup:   verifyBackup,
		}, nil), nil
	case model.ProviderPGBackRest:
		return pgbackrest.New(pgbackrest.Config{
			Binary:         cfg.Binary,
			ConfigPath:     cfg.ConfigPath,
			Stanza:         cfg.Stanza,
			Repo:           cfg.Repo,
			Env:            cfg.Env,
			WorkDir:        cfg.WorkDir,
			Timeout:        cfg.Timeout.Duration,
			RestoreTimeout: restoreCfg.Timeout.Duration,
			RedactValues:   cfg.RedactValues,
			Check:          pgBackRestCheckConfig(cfg.PGBackRest),
			Verify:         pgBackRestVerifyConfig(cfg.PGBackRestVerify),
			VerifyBackup:   verifyBackup,
		}, nil), nil
	case model.ProviderPGProbackup:
		return pgprobackup.New(pgprobackup.Config{
			Binary:         cfg.Binary,
			BackupDir:      cfg.BackupDir,
			Instance:       cfg.Instance,
			Env:            cfg.Env,
			WorkDir:        cfg.WorkDir,
			Timeout:        cfg.Timeout.Duration,
			RestoreTimeout: restoreCfg.Timeout.Duration,
			RedactValues:   cfg.RedactValues,
			Validate:       pgProbackupValidateConfig(cfg.PGProbackupValidate),
		}, nil), nil
	default:
		return nil, fmt.Errorf("provider %q is not implemented", cfg.Type)
	}
}

func pgProbackupValidateConfig(cfg config.PGProbackupValidateConfig) pgprobackup.ValidateConfig {
	return pgprobackup.ValidateConfig{
		Enabled:             cfg.Enabled,
		Timeout:             cfg.Timeout.Duration,
		WAL:                 cfg.WAL,
		SkipBlockValidation: cfg.SkipBlockValidation,
		Threads:             cfg.Threads,
		RedactValues:        append([]string{}, cfg.RedactValues...),
	}
}

func walVerifyConfig(cfg config.WALVerifyConfig) walg.WALVerifyConfig {
	return walg.WALVerifyConfig{
		Enabled:      cfg.Enabled,
		Checks:       append([]string{}, cfg.Checks...),
		BackupName:   cfg.BackupName,
		LSN:          cfg.LSN,
		Timeline:     cfg.Timeline,
		Timeout:      cfg.Timeout.Duration,
		RedactValues: append([]string{}, cfg.RedactValues...),
	}
}

func barmanVerifyConfig(cfg config.BarmanVerifyConfig) barman.BarmanVerifyConfig {
	return barman.BarmanVerifyConfig{
		Enabled:      cfg.Enabled,
		Timeout:      cfg.Timeout.Duration,
		RedactValues: append([]string{}, cfg.RedactValues...),
	}
}

func barmanManifestConfig(cfg config.BarmanManifestConfig) barman.ManifestConfig {
	return barman.ManifestConfig{
		Enabled:      cfg.Enabled,
		Timeout:      cfg.Timeout.Duration,
		RedactValues: append([]string{}, cfg.RedactValues...),
	}
}

func pgBackRestCheckConfig(cfg config.PGBackRestConfig) pgbackrest.CheckConfig {
	return pgbackrest.CheckConfig{
		Enabled:            cfg.Enabled,
		Timeout:            cfg.Timeout.Duration,
		NoArchiveCheck:     cfg.NoArchiveCheck,
		NoArchiveModeCheck: cfg.NoArchiveModeCheck,
		ArchiveTimeout:     cfg.ArchiveTimeout.Duration,
		RedactValues:       append([]string{}, cfg.RedactValues...),
	}
}

func pgBackRestVerifyConfig(cfg config.PGBackRestVerifyConfig) pgbackrest.VerifyConfig {
	return pgbackrest.VerifyConfig{
		Enabled:      cfg.Enabled,
		Timeout:      cfg.Timeout.Duration,
		Output:       cfg.Output,
		Verbose:      cfg.Verbose,
		RedactValues: append([]string{}, cfg.RedactValues...),
	}
}

func verifyBackupConfig(cfg config.VerifyBackupConfig, providerRedactions []string) pgverifybackup.Config {
	return pgverifybackup.Config{
		Enabled:       cfg.Enabled,
		Profile:       cfg.Profile,
		Binary:        cfg.Binary,
		Timeout:       cfg.Timeout.Duration,
		Format:        cfg.Format,
		ManifestPath:  cfg.ManifestPath,
		WALDirectory:  cfg.WALDirectory,
		NoParseWAL:    cfg.NoParseWAL,
		SkipChecksums: cfg.SkipChecksums,
		ExitOnError:   cfg.ExitOnError,
		Quiet:         cfg.Quiet,
		Ignore:        append([]string{}, cfg.Ignore...),
		RedactValues:  append(append([]string{}, providerRedactions...), cfg.RedactValues...),
	}
}
