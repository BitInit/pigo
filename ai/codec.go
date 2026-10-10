package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type storedBlock struct {
	Type ContentType     `json:"type"`
	Data json.RawMessage `json:"data"`
}
type storedMessage struct {
	Role    Role            `json:"role"`
	Data    json.RawMessage `json:"data"`
	Content []storedBlock   `json:"content,omitempty"`
}

// Store arguments as a string so interrupted, incomplete JSON can be preserved.
type storedCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Signature string `json:"signature,omitempty"`
}

// MarshalMessages serializes built-in message types with explicit discriminators.
// Applications with custom messages must supply their own persistence codec.
func MarshalMessages(messages []Message) ([]byte, error) {
	stored := make([]storedMessage, 0, len(messages))
	for _, input := range messages {
		var payload Message
		var content []ContentBlock
		switch m := CloneMessage(input).(type) {
		case SystemMessage:
			payload = m
		case UserMessage:
			content, m.Content = m.Content, nil
			payload = m
		case AssistantMessage:
			content, m.Content = m.Content, nil
			payload = m
		case ToolResultMessage:
			content, m.Content = m.Content, nil
			payload = m
		default:
			return nil, fmt.Errorf("ai: cannot persist message %T", input)
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		record := storedMessage{Role: payload.Role(), Data: data}
		for _, block := range content {
			var data []byte
			switch b := block.(type) {
			case TextContent, ThinkingContent, ImageContent:
				data, err = json.Marshal(b)
			case ToolCall:
				data, err = json.Marshal(storedCall{b.ID, b.Name, string(b.Arguments), b.Signature})
			default:
				return nil, fmt.Errorf("ai: cannot persist content %T", block)
			}
			if err != nil {
				return nil, err
			}
			record.Content = append(record.Content, storedBlock{Type: block.Type(), Data: data})
		}
		stored = append(stored, record)
	}
	return json.Marshal(stored)
}

// UnmarshalMessages restores built-in messages. It rejects unknown discriminators
// rather than dropping data. It does not execute tools or normalize model input.
func UnmarshalMessages(data []byte) ([]Message, error) {
	var stored []storedMessage
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, err
	}
	result := make([]Message, 0, len(stored))
	for _, record := range stored {
		if data := bytes.TrimSpace(record.Data); len(data) == 0 || data[0] != '{' {
			return nil, fmt.Errorf("ai: stored message payload must be an object")
		}
		var content []ContentBlock
		for _, block := range record.Content {
			if data := bytes.TrimSpace(block.Data); len(data) == 0 || data[0] != '{' {
				return nil, fmt.Errorf("ai: stored block payload must be an object")
			}
			var value ContentBlock
			switch block.Type {
			case ContentText:
				value = &TextContent{}
			case ContentThinking:
				value = &ThinkingContent{}
			case ContentImage:
				value = &ImageContent{}
			case ContentToolCall:
				var call storedCall
				if err := json.Unmarshal(block.Data, &call); err != nil {
					return nil, err
				}
				content = append(content, ToolCall{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments), Signature: call.Signature})
				continue
			default:
				return nil, fmt.Errorf("ai: unknown stored content type %q", block.Type)
			}
			if err := json.Unmarshal(block.Data, value); err != nil {
				return nil, err
			}
			content = append(content, value)
		}
		var value Message
		switch record.Role {
		case RoleSystem:
			value = &SystemMessage{}
		case RoleUser:
			value = &UserMessage{}
		case RoleAssistant:
			value = &AssistantMessage{}
		case RoleToolResult:
			value = &ToolResultMessage{}
		default:
			return nil, fmt.Errorf("ai: unknown stored role %q", record.Role)
		}
		if err := json.Unmarshal(record.Data, value); err != nil {
			return nil, err
		}
		switch m := value.(type) {
		case *SystemMessage:
			if len(content) != 0 {
				return nil, fmt.Errorf("ai: system message has unexpected blocks")
			}
		case *UserMessage:
			m.Content = content
		case *AssistantMessage:
			m.Content = content
		case *ToolResultMessage:
			m.Content = content
		}
		result = append(result, CloneMessage(value))
	}
	return result, nil
}
