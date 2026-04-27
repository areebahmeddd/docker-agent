package builtins

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker-agent/pkg/hooks"
)

// AddPromptFiles is the registered name of the add_prompt_files builtin.
const AddPromptFiles = "add_prompt_files"

// addPromptFiles reads each filename in args (relative to Input.Cwd) and
// joins their contents into turn_start additional context. Missing files
// are logged and skipped; surviving files still contribute.
func addPromptFiles(_ context.Context, in *hooks.Input, args []string) (*hooks.Output, error) {
	if in == nil || in.Cwd == "" || len(args) == 0 {
		return nil, nil
	}
	var parts []string
	for _, name := range args {
		additional, err := readPromptFiles(in.Cwd, name)
		if err != nil {
			slog.Warn("reading prompt file", "file", name, "error", err)
			continue
		}
		parts = append(parts, additional...)
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return turnStartContext(strings.Join(parts, "\n\n")), nil
}

// readPromptFiles looks for a prompt file in the working directory hierarchy
// and in the user's home folder. If found in both locations, both contents are returned.
// The working directory content is returned first, followed by the home folder content.
func readPromptFiles(workDir, filename string) ([]string, error) {
	var results []string

	// Look in the working directory hierarchy
	workDirPath := findFileInHierarchy(workDir, filename)
	if workDirPath != "" {
		content, err := os.ReadFile(workDirPath)
		if err != nil {
			return nil, err
		}
		results = append(results, string(content))
	}

	// Look in the home folder (skip if already found there)
	if homeDir, err := os.UserHomeDir(); err == nil {
		homePath := filepath.Join(homeDir, filename)
		if homePath != workDirPath && isFile(homePath) {
			content, err := os.ReadFile(homePath)
			if err != nil {
				return nil, err
			}
			results = append(results, string(content))
		}
	}

	return results, nil
}

// findFileInHierarchy searches for a file starting from the given directory
// and traversing up the directory tree. Returns the path if found, empty string otherwise.
func findFileInHierarchy(startDir, filename string) string {
	current, err := filepath.Abs(startDir)
	if err != nil {
		return ""
	}

	for {
		path := filepath.Join(current, filename)
		if isFile(path) {
			return path
		}

		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

// isFile returns true if path exists and is a regular file.
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
