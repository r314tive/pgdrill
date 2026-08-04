package doccheck

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var markdownLink = regexp.MustCompile(`!?\[[^]]*\]\(([^)[:space:]]+)(?:[[:space:]]+[^)]*)?\)`)

// Issue identifies one invalid repository-local Markdown link.
type Issue struct {
	Source      string
	Line        int
	Destination string
	Reason      string
}

// CheckRepository validates local inline links in every Markdown file under root.
func CheckRepository(root string) ([]Issue, error) {
	var markdownFiles []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && path != root && ignoredDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(path), ".md") {
			markdownFiles = append(markdownFiles, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk documentation: %w", err)
	}
	sort.Strings(markdownFiles)

	var issues []Issue
	for _, path := range markdownFiles {
		fileIssues, err := checkFile(root, path)
		if err != nil {
			return nil, err
		}
		issues = append(issues, fileIssues...)
	}
	return issues, nil
}

func ignoredDirectory(name string) bool {
	switch name {
	case ".git", ".cache", ".notes", ".state", ".terraform", "bin", "dist":
		return true
	default:
		return false
	}
}

func checkFile(root, path string) ([]Issue, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	relSource, err := filepath.Rel(root, path)
	if err != nil {
		return nil, fmt.Errorf("resolve source path %s: %w", path, err)
	}

	var issues []Issue
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	inFence := false
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}

		for _, match := range markdownLink.FindAllStringSubmatch(line, -1) {
			destination := strings.Trim(match[1], "<>")
			issue := validateDestination(root, path, relSource, lineNumber, destination)
			if issue != nil {
				issues = append(issues, *issue)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return issues, nil
}

func validateDestination(root, sourcePath, relSource string, line int, destination string) *Issue {
	if destination == "" || strings.HasPrefix(destination, "#") || strings.HasPrefix(destination, "//") {
		return nil
	}
	if isWindowsAbsolutePath(destination) {
		return &Issue{
			Source: relSource, Line: line, Destination: destination,
			Reason: "repository documentation must not use an absolute local path",
		}
	}

	parsed, err := url.Parse(destination)
	if err != nil {
		return &Issue{Source: relSource, Line: line, Destination: destination, Reason: err.Error()}
	}
	if strings.EqualFold(parsed.Scheme, "file") {
		return &Issue{
			Source: relSource, Line: line, Destination: destination,
			Reason: "repository documentation must not use a local file URL",
		}
	}
	if parsed.IsAbs() || parsed.Host != "" || parsed.Path == "" {
		return nil
	}

	decoded, err := url.PathUnescape(parsed.Path)
	if err != nil {
		return &Issue{Source: relSource, Line: line, Destination: destination, Reason: err.Error()}
	}
	if filepath.IsAbs(decoded) {
		return &Issue{
			Source: relSource, Line: line, Destination: destination,
			Reason: "repository documentation must not use an absolute local path",
		}
	}

	target := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), filepath.FromSlash(decoded)))
	relTarget, err := filepath.Rel(root, target)
	if err != nil || relTarget == ".." || strings.HasPrefix(relTarget, ".."+string(filepath.Separator)) {
		return &Issue{
			Source: relSource, Line: line, Destination: destination,
			Reason: "target escapes the repository",
		}
	}
	if _, err := os.Stat(target); err != nil {
		return &Issue{
			Source: relSource, Line: line, Destination: destination,
			Reason: err.Error(),
		}
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return &Issue{Source: relSource, Line: line, Destination: destination, Reason: err.Error()}
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return &Issue{Source: relSource, Line: line, Destination: destination, Reason: err.Error()}
	}
	resolvedRelative, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
		return &Issue{
			Source: relSource, Line: line, Destination: destination,
			Reason: "target resolves outside the repository",
		}
	}
	return nil
}

func isWindowsAbsolutePath(value string) bool {
	if strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 &&
		((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) &&
		value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}
