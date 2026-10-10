package ai

import "encoding/json"

// Tool describes a callable tool to a model. Execution belongs to package agent.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Request is one model invocation. Provider, model, and generation settings are
// owned by the configured model function, not by the agent loop.
type Request struct {
	Messages []Message
	Tools    []Tool
}
