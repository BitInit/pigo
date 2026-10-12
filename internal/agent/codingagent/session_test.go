package codingagent_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bitinit/pigo/internal/agent/codingagent"
	"github.com/bitinit/pigo/internal/core/agentloop"
	"github.com/bitinit/pigo/internal/core/ai"
)

func TestCodingSessionReadsEditsWritesVerifiesAndRestores(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Use exact edits and verify your work."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "greeting.txt"), []byte("hello old\n"), 0755); err != nil {
		t.Fatal(err)
	}
	step := 0
	calls := []ai.ToolCall{
		{ID: "read1", Name: "read", Arguments: json.RawMessage(`{"path":"greeting.txt"}`)},
		{ID: "edit1", Name: "edit", Arguments: json.RawMessage(`{"path":"greeting.txt","oldText":"old","newText":"new"}`)},
		{ID: "write1", Name: "write", Arguments: json.RawMessage(`{"path":"notes/result.txt","content":"updated"}`)},
		{ID: "check1", Name: "bash", Arguments: json.RawMessage(`{"command":"test \"$(cat greeting.txt)\" = 'hello new' && printf verified"}`)},
	}
	options := codingagent.Options{Directory: dir, Agent: agentloop.Options{Stream: func(_ context.Context, request ai.Request, _ func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
		if !strings.Contains(request.Messages[0].(ai.SystemMessage).Content, "Use exact edits") {
			t.Fatal("project instructions missing")
		}
		if step > 0 {
			result := request.Messages[len(request.Messages)-1].(ai.ToolResultMessage)
			if result.IsError {
				t.Fatalf("tool failed: %+v", result)
			}
			if step == 4 && result.Content[0].(ai.TextContent).Text != "verified" {
				t.Fatalf("verification failed: %+v", result)
			}
		}
		if step < len(calls) {
			call := calls[step]
			step++
			return ai.AssistantMessage{StopReason: ai.StopReasonToolUse, Content: []ai.ContentBlock{call}}, nil
		}
		return ai.AssistantMessage{StopReason: ai.StopReasonStop, Content: []ai.ContentBlock{ai.TextContent{Text: "Updated and verified."}}}, nil
	}}}
	session, err := codingagent.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Agent.Prompt(context.Background(), ai.UserMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "update greeting"}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "greeting.txt"))
	if err != nil || string(data) != "hello new\n" {
		t.Fatalf("edit result=%s err=%v", data, err)
	}
	info, err := os.Stat(filepath.Join(dir, "greeting.txt"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("file mode changed: %v", err)
	}
	session.Agent.FollowUp(ai.UserMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "next"}}})
	path := filepath.Join(dir, "session.json")
	if err := session.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("session mode: %v", err)
	}
	// Loading cannot invoke the model or replay tools.
	options.Agent.Stream = func(context.Context, ai.Request, func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
		t.Fatal("load replayed execution")
		return ai.AssistantMessage{}, nil
	}
	restored, err := codingagent.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Load(path); err != nil {
		t.Fatal(err)
	}
	state := restored.Agent.Snapshot()
	if len(state.Messages) != 11 || len(state.FollowUp) != 1 {
		t.Fatalf("restore messages=%d followup=%d", len(state.Messages), len(state.FollowUp))
	}
	options.Directory = t.TempDir()
	other, err := codingagent.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Load(path); err == nil {
		t.Fatal("accepted a session from another directory")
	}
}
