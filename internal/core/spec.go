package core

import (
	"fmt"

	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/runspec"
)

func validateExecutableSpec(spec runspec.Spec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if schema := spec.Document().SchemaVersion; schema != model.CurrentDrillSpecSchemaVersion {
		return fmt.Errorf(
			"schema_version %q is read-only; execution requires %q",
			schema,
			model.CurrentDrillSpecSchemaVersion,
		)
	}
	return nil
}
