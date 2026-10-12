// Package ai defines provider-independent model messages.
package ai

import "encoding/json"

// Role identifies a model role or an application-defined message category.
// Applications may define additional roles without changing this package.
type Role string

const (
	RoleSystem     Role = "system"
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "toolResult"
)

// Message identifies a message without prescribing its payload.
// Implementing Message does not imply that a provider supports the concrete type.
// Provider adapters must validate concrete types and content before encoding them.
//
// Role is a method, not a serialized field. A separate codec must encode the role
// and reconstruct concrete message and content types when persisting a transcript.
type Message interface {
	Role() Role
}

// SystemMessage contains model instructions. Timestamp is Unix milliseconds;
// zero means unspecified.
type SystemMessage struct {
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp,omitempty"`
}

func (SystemMessage) Role() Role { return RoleSystem }

// UserMessage contains user text and images.
type UserMessage struct {
	Content   []ContentBlock `json:"content"`
	Timestamp int64          `json:"timestamp,omitempty"`
}

func (UserMessage) Role() Role { return RoleUser }

// AssistantMessage preserves text, reasoning, and tool calls in source order.
// Pending messages may contain incomplete output while streaming.
type AssistantMessage struct {
	Content      []ContentBlock `json:"content"`
	Provider     string         `json:"provider"`
	Model        string         `json:"model"`
	ResponseID   string         `json:"responseId,omitempty"`
	Usage        Usage          `json:"usage"`
	StopReason   StopReason     `json:"stopReason"`
	ErrorMessage string         `json:"errorMessage,omitempty"`
	Timestamp    int64          `json:"timestamp,omitempty"`
	// ProviderData preserves provider-specific replay metadata as valid JSON.
	ProviderData json.RawMessage `json:"providerData,omitempty"`
}

func (AssistantMessage) Role() Role { return RoleAssistant }

// ToolResultMessage answers a tool call identified by ToolCallID.
// Content contains text and images visible to the model; Details is application
// data for storage or display and is not automatically sent to the model.
type ToolResultMessage struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Content    []ContentBlock  `json:"content"`
	IsError    bool            `json:"isError"`
	Details    json.RawMessage `json:"details,omitempty"`
	// Usage belongs to this tool execution, not the conversational model call.
	Usage     *Usage `json:"usage,omitempty"`
	Timestamp int64  `json:"timestamp,omitempty"`
}

func (ToolResultMessage) Role() Role { return RoleToolResult }
