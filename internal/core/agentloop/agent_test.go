package agentloop_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bitinit/pigo/internal/core/agentloop"
	"github.com/bitinit/pigo/internal/core/ai"
)

func TestAgentConcurrentControlAndSettlement(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	ending, settle := make(chan struct{}), make(chan struct{})
	requests := 0
	a, err := agentloop.New(agentloop.Options{
		Stream: func(ctx context.Context, request ai.Request, update func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
			requests++
			last := request.Messages[len(request.Messages)-1].(ai.UserMessage).Content[0].(ai.TextContent).Text
			want := []string{"first", "steering", "follow-up"}[requests-1]
			if last != want {
				t.Errorf("got %q want %q", last, want)
			}
			if requests == 1 {
				close(entered)
				<-release
			}
			update(answer("partial"))
			return answer("done"), nil
		},
		OnEvent: func(event agentloop.Event) {
			if event.Type == agentloop.EventAgentEnd {
				close(ending)
				<-settle
			}
		},
	}, ai.SystemMessage{Content: "system"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Prompt(context.Background(), user("first")) }()
	<-entered
	if err := a.Prompt(context.Background(), user("concurrent")); !errors.Is(err, agentloop.ErrBusy) {
		t.Fatalf("concurrent prompt: %v", err)
	}
	if err := a.Reset(); !errors.Is(err, agentloop.ErrBusy) {
		t.Fatalf("active reset: %v", err)
	}
	if err := a.ReplaceMessages(nil); !errors.Is(err, agentloop.ErrBusy) {
		t.Fatalf("active restore: %v", err)
	}
	a.Steer(user("steering"))
	a.FollowUp(user("follow-up"))
	snapshot := a.Snapshot()
	snapshot.Messages[1].(ai.UserMessage).Content[0] = ai.TextContent{Text: "mutated"}
	if a.Snapshot().Messages[1].(ai.UserMessage).Content[0].(ai.TextContent).Text != "first" {
		t.Fatal("snapshot mutated owned history")
	}
	close(release)
	<-ending
	if !a.Snapshot().Running {
		t.Fatal("agent became idle before callback settlement")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := a.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait ignored callback: %v", err)
	}
	close(settle)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := a.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := a.Snapshot()
	if state.Running || state.Err != nil || len(state.Messages) != 7 || requests != 3 {
		t.Fatalf("incorrect settled state: %+v", state)
	}
	if err := a.Reset(); err != nil {
		t.Fatal(err)
	}
	if state := a.Snapshot(); len(state.Messages) != 1 || len(state.Steering)+len(state.FollowUp) != 0 {
		t.Fatalf("reset: %+v", state)
	}
}

func TestAgentAbortPreservesResponse(t *testing.T) {
	entered := make(chan struct{})
	a, err := agentloop.New(agentloop.Options{Stream: func(ctx context.Context, _ ai.Request, update func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
		partial := answer("partial")
		update(partial)
		close(entered)
		<-ctx.Done()
		return partial, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Prompt(context.Background(), user("hello")) }()
	<-entered
	if a.Snapshot().Partial == nil {
		t.Fatal("no streaming snapshot")
	}
	a.Abort()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("abort: %v", err)
	}
	s := a.Snapshot()
	if !errors.Is(s.Err, context.Canceled) || s.Running || s.Messages[1].(ai.AssistantMessage).StopReason != ai.StopReasonAborted {
		t.Fatalf("aborted state: %+v", s)
	}
}

func TestAgentContinueDoesNotPreemptToolResultsWithFollowUp(t *testing.T) {
	requests := 0
	a, err := agentloop.New(agentloop.Options{Stream: func(_ context.Context, request ai.Request, _ func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
		requests++
		want := ai.RoleToolResult
		if requests == 2 {
			want = ai.RoleUser
		}
		if got := request.Messages[len(request.Messages)-1].Role(); got != want {
			t.Fatalf("request %d tail=%s want=%s", requests, got, want)
		}
		return answer("done"), nil
	}}, user("read"), ai.AssistantMessage{Content: []ai.ContentBlock{call("id", "read")}, StopReason: ai.StopReasonToolUse}, ai.ToolResultMessage{ToolCallID: "id", ToolName: "read"})
	if err != nil {
		t.Fatal(err)
	}
	a.FollowUp(user("next"))
	if err := a.Continue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}
