package release

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func ExtractChangelog(reader io.Reader, version string) (string, error) {
	if err := ValidateVersion(version); err != nil {
		return "", err
	}
	heading := "## [" + strings.TrimPrefix(version, "v") + "]"
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	found := false
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		if !found {
			if line == heading || strings.HasPrefix(line, heading+" - ") {
				found = true
			}
			continue
		}
		if strings.HasPrefix(line, "## [") {
			break
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read changelog: %w", err)
	}
	if !found {
		return "", fmt.Errorf("changelog entry %s not found", heading)
	}
	body := strings.TrimSpace(strings.Join(lines, "\n"))
	if body == "" {
		return "", fmt.Errorf("changelog entry %s is empty", heading)
	}
	return body + "\n", nil
}

func WriteReleaseNotes(changelogPath, outputPath, version string) error {
	file, err := os.Open(changelogPath)
	if err != nil {
		return fmt.Errorf("open changelog: %w", err)
	}
	notes, extractErr := ExtractChangelog(file, version)
	closeErr := file.Close()
	if extractErr != nil {
		return extractErr
	}
	if closeErr != nil {
		return fmt.Errorf("close changelog: %w", closeErr)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("create release notes directory: %w", err)
	}
	if err := os.WriteFile(outputPath, []byte(notes), 0o644); err != nil {
		return fmt.Errorf("write release notes: %w", err)
	}
	return nil
}
