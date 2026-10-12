package codingagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bitinit/pigo/internal/core/ai"
	"github.com/bitinit/pigo/internal/storage"
)

func systemMessage(directory, prompt string) (ai.SystemMessage, error) {
	if prompt == "" {
		prompt = defaultSystemPrompt
	}
	var dirs []string
	for path := directory; ; path = filepath.Dir(path) {
		dirs = append(dirs, path)
		if filepath.Dir(path) == path {
			break
		}
	}
	var contextText strings.Builder
	for i := len(dirs) - 1; i >= 0; i-- {
		path := filepath.Join(dirs[i], "AGENTS.md")
		data, readErr := storage.ReadText(path, 256<<10)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return ai.SystemMessage{}, fmt.Errorf("codingagent: instructions %s: %w", path, readErr)
		}
		fmt.Fprintf(&contextText, "\n\nProject instructions from %s:\n%s", path, data)
	}
	return ai.SystemMessage{Content: prompt + "\n\nWorking directory: " + directory + contextText.String()}, nil
}
