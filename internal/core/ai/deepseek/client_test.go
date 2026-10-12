package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitinit/pigo/internal/core/agentloop"
	"github.com/bitinit/pigo/internal/core/ai"
)

func TestStreamToolRoundTrip(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		var input request
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if !input.Stream || !input.StreamOptions.IncludeUsage || len(input.Tools) != 1 || input.Tools[0].Function.Name != "read" {
			t.Errorf("invalid request: %+v", input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, `: heartbeat

data: {"id":"response1","choices":[{"index":0,"delta":{"reasoning_content":"inspect first"}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call1","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"main.go\"}"}}]}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_cache_hit_tokens":4,"total_tokens":15,"completion_tokens_details":{"reasoning_tokens":3}}}

data: [DONE]

`)
		} else {
			if len(input.Messages) != 3 || input.Messages[1].Reasoning != "inspect first" || input.Messages[2].Role != "tool" || input.Messages[2].ToolCallID != "call1" {
				t.Errorf("lost tool/reasoning context: %+v", input.Messages)
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer server.Close()
	client, err := New(Config{APIKey: "test-key", Model: "test-model", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []ai.AssistantMessage
	history, err := agentloop.Run(context.Background(), []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "read"}}}}, agentloop.Options{
		Stream: func(ctx context.Context, request ai.Request, update func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
			return client.Stream(ctx, request, func(partial ai.AssistantMessage) { snapshots = append(snapshots, partial); update(partial) })
		},
		Tools: []agentloop.Tool{{Tool: ai.Tool{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}, Execute: func(_ context.Context, call ai.ToolCall, _ func(ai.ToolResultMessage)) (ai.ToolResultMessage, error) {
			if string(call.Arguments) != `{"path":"main.go"}` {
				t.Fatalf("arguments were not assembled: %s", call.Arguments)
			}
			return ai.ToolResultMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "package main"}}}, nil
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(history) != 4 {
		t.Fatalf("requests=%d history=%d", requests, len(history))
	}
	assistant := history[1].(ai.AssistantMessage)
	if assistant.Usage.Input != 6 || assistant.Usage.CacheRead != 4 || *assistant.Usage.Reasoning != 3 {
		t.Fatalf("usage: %+v", assistant.Usage)
	}
	if len(snapshots[0].Content) != 1 || string(snapshots[1].Content[1].(ai.ToolCall).Arguments) != `{"path":` {
		t.Fatal("later deltas mutated earlier snapshots")
	}
}

func TestStreamProtocolFailuresAndHTTPError(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"missing-done", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"stop\"}]}\n\n", 200},
		{"missing-finish", "data: [DONE]\n\n", 200},
		{"invalid-json", "data: broken\n\n", 200},
		{"server-error", "data: {\"error\":{\"message\":\"failed\"}}\n\n", 200},
		{"filtered", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n", 200},
		{"http", `{"error":{"message":"rate limited"}}`, 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client, err := New(Config{APIKey: "test", Model: "test", BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Stream(context.Background(), ai.Request{Messages: []ai.Message{ai.UserMessage{}}}, nil)
			if err == nil || result.StopReason != ai.StopReasonError {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if tc.name == "missing-done" && result.Content[0].(ai.TextContent).Text != "partial" {
				t.Fatal("lost partial response")
			}
		})
	}
}

func TestStreamCancellationAndUnsupportedContent(t *testing.T) {
	client, err := New(Config{APIKey: "test", Model: "test", BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Stream(ctx, ai.Request{Messages: []ai.Message{ai.UserMessage{}}}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	_, err = client.Stream(context.Background(), ai.Request{Messages: []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{ai.ImageContent{}}}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported content") {
		t.Fatalf("unsupported input: %v", err)
	}
}

func TestStreamCancellationDuringResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := New(Config{APIKey: "test", Model: "test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := client.Stream(ctx, ai.Request{Messages: []ai.Message{ai.UserMessage{}}}, func(ai.AssistantMessage) { cancel() })
	if !errors.Is(err, context.Canceled) || result.StopReason != ai.StopReasonAborted || result.Content[0].(ai.TextContent).Text != "partial" {
		t.Fatalf("cancellation lost state: %+v %v", result, err)
	}
}
