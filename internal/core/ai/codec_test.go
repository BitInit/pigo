package ai_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/bitinit/pigo/internal/core/ai"
)

func TestMessageRoundTripAndOwnership(t *testing.T) {
	reasoning := int64(3)
	messages := []ai.Message{
		&ai.SystemMessage{Content: "system"},
		ai.UserMessage{Content: []ai.ContentBlock{&ai.TextContent{Text: "hello"}, ai.ImageContent{Data: "abc", MIMEType: "image/png"}}},
		&ai.AssistantMessage{Content: []ai.ContentBlock{ai.ThinkingContent{Text: "reasoning", Signature: "sig"}, &ai.ToolCall{ID: "id", Name: "write", Arguments: json.RawMessage(`{"incomplete":`)}}, StopReason: ai.StopReasonAborted, Usage: ai.Usage{Reasoning: &reasoning, Cost: &ai.UsageCost{Total: 1}}, ProviderData: json.RawMessage(`{"id":"opaque"}`)},
		ai.ToolResultMessage{ToolCallID: "id", Details: json.RawMessage(`{"code":1}`), Content: []ai.ContentBlock{ai.TextContent{Text: "failed"}}, IsError: true},
	}
	data, err := ai.MarshalMessages(messages)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := ai.UnmarshalMessages(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, ai.CloneMessages(messages)) {
		t.Fatalf("round trip lost fields:\n%#v\n%#v", restored, messages)
	}
	copy := ai.CloneMessages(restored)
	assistant := copy[2].(ai.AssistantMessage)
	*assistant.Usage.Reasoning = 99
	assistant.Usage.Cost.Total = 99
	assistant.ProviderData[0] = '['
	assistant.Content[1].(ai.ToolCall).Arguments[0] = '['
	original := restored[2].(ai.AssistantMessage)
	if *original.Usage.Reasoning != 3 || original.Usage.Cost.Total != 1 || original.ProviderData[0] != '{' || original.Content[1].(ai.ToolCall).Arguments[0] != '{' {
		t.Fatal("clone shares mutable built-in payloads")
	}
}

func TestUnknownAndCorruptStoredMessagesFail(t *testing.T) {
	for _, data := range []string{
		`[{"role":"custom","data":{}}]`,
		`[{"role":"user","data":null}]`,
		`[{"role":"user","data":{},"content":[{"type":"unknown","data":{}}]}]`,
		`[{"role":"user","data":{},"content":[{"type":"text","data":null}]}]`,
	} {
		if _, err := ai.UnmarshalMessages([]byte(data)); err == nil {
			t.Fatalf("accepted corrupt record: %s", data)
		}
	}
}
