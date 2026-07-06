package report

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/r314tive/pgdrill/internal/model"
)

type JSONFileSink struct {
	Path string
}

func (s JSONFileSink) Write(ctx context.Context, result model.DrillResult) error {
	if s.Path == "" {
		return fmt.Errorf("report path is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create report directory %s: %w", dir, err)
	}

	file, err := os.CreateTemp(dir, "."+filepath.Base(s.Path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary report file: %w", err)
	}

	tmpPath := file.Name()
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := WriteJSON(file, result); err != nil {
		_ = file.Close()
		return fmt.Errorf("write report json: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync report file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close report file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, s.Path); err != nil {
		return fmt.Errorf("replace report file %s: %w", s.Path, err)
	}

	keepTemp = true
	return nil
}

func ReadJSONFile(path string) (model.DrillResult, error) {
	if path == "" {
		return model.DrillResult{}, fmt.Errorf("report path is required")
	}

	file, err := os.Open(path)
	if err != nil {
		return model.DrillResult{}, fmt.Errorf("open report file %s: %w", path, err)
	}
	defer file.Close()

	result, err := ReadJSON(file)
	if err != nil {
		return model.DrillResult{}, fmt.Errorf("read report file %s: %w", path, err)
	}
	return result, nil
}

func ReadJSON(reader io.Reader) (model.DrillResult, error) {
	var result model.DrillResult
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&result); err != nil {
		return model.DrillResult{}, fmt.Errorf("parse report json: %w", err)
	}
	return result, nil
}

func WriteJSON(writer io.Writer, result model.DrillResult) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("encode report json: %w", err)
	}
	return nil
}
