package codingagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bitinit/pigo/agent"
	"github.com/bitinit/pigo/ai"
	"github.com/bitinit/pigo/codingagent"
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
	options := codingagent.Options{Directory: dir, Agent: agent.Options{Stream: func(_ context.Context, request ai.Request, _ func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
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

func TestCodingToolsRejectInvalidEditsAndArguments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("same same"), 0644); err != nil {
		t.Fatal(err)
	}
	tools, err := codingagent.NewTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, args string }{
		{"edit", `{"path":"file.txt","oldText":"same","newText":"changed"}`},
		{"edit", `{"path":"file.txt","oldText":"absent","newText":"changed"}`},
		{"write", `{"path":"file.txt"}`},
		{"write", `{"path":"file.txt","content":"changed","unexpected":true}`},
		{"read", `{"path":"file.txt","offset":0}`},
		{"read", `{"path":"file.txt","offset":10}`},
	} {
		for _, tool := range tools {
			if tool.Name == tc.name {
				if _, err := tool.Execute(context.Background(), ai.ToolCall{Arguments: json.RawMessage(tc.args)}, nil); err == nil {
					t.Errorf("accepted %s %s", tc.name, tc.args)
				}
			}
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "same same" {
		t.Fatalf("invalid arguments changed file: %s %v", data, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "binary"), []byte{0, 1, 2}, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := tools[0].Execute(context.Background(), ai.ToolCall{Arguments: json.RawMessage(`{"path":"binary"}`)}, nil); err == nil {
		t.Fatal("read accepted binary input")
	}
}

func TestBashOutputLimitsAndTimeout(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	tools, err := codingagent.NewTools(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bash := tools[3]
	updates := 0
	result, err := bash.Execute(context.Background(), ai.ToolCall{Arguments: json.RawMessage(`{"command":"printf '%070000d' 0"}`)}, func(result ai.ToolResultMessage) { updates++ })
	if err != nil || updates == 0 || !strings.Contains(result.Content[0].(ai.TextContent).Text, "truncated") {
		t.Fatalf("output limit: %+v %v", result, err)
	}
	start := time.Now()
	_, err = bash.Execute(context.Background(), ai.ToolCall{Arguments: json.RawMessage(`{"command":"sleep 30 & wait","timeoutSeconds":1}`)}, nil)
	if err == nil || !strings.Contains(err.Error(), "timed out") || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("tool timeout must be recoverable by model: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout did not stop command promptly")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = bash.Execute(ctx, ai.ToolCall{Arguments: json.RawMessage(`{"command":"printf unexpected"}`)}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation lost: %v", err)
	}
}
