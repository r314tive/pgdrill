package planner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func LoadFile(path string) (Fleet, error) {
	if strings.TrimSpace(path) == "" {
		return Fleet{}, fmt.Errorf("fleet file path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return Fleet{}, fmt.Errorf("open fleet file %s: %w", path, err)
	}
	defer file.Close()
	fleet, err := Load(file, formatFromPath(path))
	if err != nil {
		return Fleet{}, fmt.Errorf("load fleet file %s: %w", path, err)
	}
	return fleet, nil
}

func Load(reader io.Reader, format string) (Fleet, error) {
	if reader == nil {
		return Fleet{}, fmt.Errorf("fleet input is required")
	}
	payload, err := io.ReadAll(io.LimitReader(reader, MaxFleetBytes+1))
	if err != nil {
		return Fleet{}, fmt.Errorf("read fleet input: %w", err)
	}
	if len(payload) > MaxFleetBytes {
		return Fleet{}, fmt.Errorf("fleet input exceeds %d bytes", MaxFleetBytes)
	}

	var fleet Fleet
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&fleet); err != nil {
			return Fleet{}, fmt.Errorf("parse json fleet: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return Fleet{}, fmt.Errorf("parse json fleet: multiple JSON values")
			}
			return Fleet{}, fmt.Errorf("parse json fleet trailing data: %w", err)
		}
	case "yaml", "yml", "":
		decoder := yaml.NewDecoder(bytes.NewReader(payload))
		decoder.KnownFields(true)
		if err := decoder.Decode(&fleet); err != nil {
			return Fleet{}, fmt.Errorf("parse yaml fleet: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return Fleet{}, fmt.Errorf("parse yaml fleet: multiple YAML documents")
			}
			return Fleet{}, fmt.Errorf("parse yaml fleet trailing data: %w", err)
		}
	default:
		return Fleet{}, fmt.Errorf("unsupported fleet format %q", format)
	}

	fleet = normalizeFleet(fleet)
	if err := fleet.Validate(); err != nil {
		return Fleet{}, err
	}
	return fleet, nil
}

func formatFromPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	default:
		return "yaml"
	}
}
