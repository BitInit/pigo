package ai

import "encoding/json"

// ContentType identifies a content block's payload format.
type ContentType string

const (
	ContentText     ContentType = "text"
	ContentThinking ContentType = "thinking"
	ContentImage    ContentType = "image"
	ContentToolCall ContentType = "toolCall"
)

// ContentBlock is open to extension, like Message. Provider adapters must check
// the concrete block type and whether it is supported for the enclosing role.
// Type, like Message.Role, is not automatically included by encoding/json.
type ContentBlock interface {
	Type() ContentType
}

type TextContent struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

func (TextContent) Type() ContentType { return ContentText }

// ThinkingContent preserves reasoning and opaque replay signatures.
type ThinkingContent struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
	Redacted  bool   `json:"redacted,omitempty"`
}

func (ThinkingContent) Type() ContentType { return ContentThinking }

type ImageContent struct {
	// Data is base64-encoded image data.
	Data     string `json:"data"`
	MIMEType string `json:"mimeType"`
}

func (ImageContent) Type() ContentType { return ContentImage }

type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Arguments must be a complete JSON object before tool execution. Streaming
	// JSON fragments belong to stream events, not executable tool calls.
	Arguments json.RawMessage `json:"arguments"`
	Signature string          `json:"signature,omitempty"`
}

func (ToolCall) Type() ContentType { return ContentToolCall }
