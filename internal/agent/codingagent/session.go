// Package codingagent composes a general agent with coding instructions and tools.
package codingagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/bitinit/pigo/internal/agent/codingagent/tools"
	"github.com/bitinit/pigo/internal/core/agentloop"
	"github.com/bitinit/pigo/internal/core/ai"
	"github.com/bitinit/pigo/internal/storage"
)

// Options selects coding resources and the underlying agent's execution settings.
type Options struct {
	Directory string
	// SystemPrompt replaces the default coding instructions. Working directory
	// and ancestor AGENTS.md instructions are appended in either case.
	SystemPrompt string
	// Agent supplies the model function, event handler, and turn limit. Nil Tools
	// selects the four coding tools; a non-nil slice replaces that default set.
	Agent agentloop.Options
}

// Session contains application resources; conversation execution belongs to Agent.
// Save and Load are for idle sessions. Callers serialize them with other mutations.
type Session struct {
	Agent     *agentloop.Agent
	directory string
	system    ai.SystemMessage
}

// New loads project instructions and creates a session without calling the model.
func New(options Options) (*Session, error) {
	if options.Directory == "" {
		options.Directory = "."
	}
	directory, err := filepath.Abs(options.Directory)
	if err != nil {
		return nil, err
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("codingagent: working directory is not a directory")
	}
	if options.Agent.Tools == nil {
		options.Agent.Tools, err = tools.New(directory)
		if err != nil {
			return nil, err
		}
	}
	system, err := systemMessage(directory, options.SystemPrompt)
	if err != nil {
		return nil, err
	}
	a, err := agentloop.New(options.Agent, system)
	if err != nil {
		return nil, err
	}
	return &Session{Agent: a, directory: directory, system: system}, nil
}

type sessionFile struct {
	Version   int             `json:"version"`
	Directory string          `json:"directory"`
	Messages  json.RawMessage `json:"messages"`
	Steering  json.RawMessage `json:"steering"`
	FollowUp  json.RawMessage `json:"followUp"`
}

// Save atomically writes built-in messages and pending queues with mode 0600.
// Provider configuration and executable tool implementations are not serialized.
// Conversation text and tool output are stored in full.
func (s *Session) Save(path string) error {
	state := s.Agent.Snapshot()
	if state.Running {
		return agentloop.ErrBusy
	}
	record := sessionFile{Version: 1, Directory: s.directory}
	var err error
	if record.Messages, err = ai.MarshalMessages(state.Messages); err != nil {
		return err
	}
	if record.Steering, err = ai.MarshalMessages(state.Steering); err != nil {
		return err
	}
	if record.FollowUp, err = ai.MarshalMessages(state.FollowUp); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 32<<20 {
		return errors.New("codingagent: session exceeds 32 MiB")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return storage.WriteAtomic(context.Background(), path, data, 0600)
}

// Load restores data only; tool calls are never replayed. It requires the same
// working directory and refreshes the initial system prompt from current project
// instructions. Custom message codecs can be implemented by the embedding app.
func (s *Session) Load(path string) error {
	if s.Agent.Snapshot().Running {
		return agentloop.ErrBusy
	}
	data, err := storage.ReadText(path, 32<<20)
	if err != nil {
		return err
	}
	var record sessionFile
	if err := json.Unmarshal(data, &record); err != nil {
		return err
	}
	if record.Version != 1 || record.Directory != s.directory {
		return errors.New("codingagent: session version or working directory mismatch")
	}
	messages, err := ai.UnmarshalMessages(record.Messages)
	if err != nil {
		return err
	}
	steering, err := ai.UnmarshalMessages(record.Steering)
	if err != nil {
		return err
	}
	followUp, err := ai.UnmarshalMessages(record.FollowUp)
	if err != nil {
		return err
	}
	if len(messages) == 0 || messages[0].Role() != ai.RoleSystem {
		return errors.New("codingagent: session has no initial system message")
	}
	messages[0] = s.system
	if err := s.Agent.ReplaceMessages(messages); err != nil {
		return err
	}
	s.Agent.Steer(steering...)
	s.Agent.FollowUp(followUp...)
	return nil
}
