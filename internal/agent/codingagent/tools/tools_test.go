package tools_test

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

	"github.com/bitinit/pigo/internal/agent/codingagent/tools"
	"github.com/bitinit/pigo/internal/core/ai"
)

func TestCodingToolsRejectInvalidEditsAndArguments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("same same"), 0644); err != nil {
		t.Fatal(err)
	}
	toolset, err := tools.New(dir)
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
		for _, tool := range toolset {
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
	if _, err := toolset[0].Execute(context.Background(), ai.ToolCall{Arguments: json.RawMessage(`{"path":"binary"}`)}, nil); err == nil {
		t.Fatal("read accepted binary input")
	}
}

func TestBashOutputLimitsAndTimeout(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	toolset, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bash := toolset[3]
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
