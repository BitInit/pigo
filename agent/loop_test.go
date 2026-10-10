package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/bitinit/pigo/agent"
	"github.com/bitinit/pigo/ai"
)

func user(text string) ai.UserMessage {
	return ai.UserMessage{Content: []ai.ContentBlock{ai.TextContent{Text: text}}}
}

func answer(text string) ai.AssistantMessage {
	return ai.AssistantMessage{StopReason: ai.StopReasonStop, Content: []ai.ContentBlock{ai.TextContent{Text: text}}}
}

func call(id, name string) ai.ToolCall {
	return ai.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(`{"path":"main.go"}`)}
}

func declaration(name string) ai.Tool {
	return ai.Tool{Name: name, Parameters: json.RawMessage(`{"type":"object"}`)}
}

func TestRunToolCycle(t *testing.T) {
	ctx := context.Background()
	original := make([]ai.Message, 1, 8)
	original[0] = user("read the file")
	var events []agent.EventType
	var executed []string
	modelCalls := 0
	streamReturned := false
	var historyLengths []int
	tools := []agent.Tool{{
		Tool: declaration("read"),
		Execute: func(ctx context.Context, c ai.ToolCall, update func(ai.ToolResultMessage)) (ai.ToolResultMessage, error) {
			if !streamReturned {
				t.Fatal("tool executed before final model response")
			}
			executed = append(executed, c.ID)
			update(ai.ToolResultMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "reading"}}})
			return ai.ToolResultMessage{ToolCallID: "ignored", Content: []ai.ContentBlock{ai.TextContent{Text: "package main"}}}, nil
		},
	}}
	history, err := agent.Run(ctx, original, agent.Options{
		Tools: tools,
		Stream: func(ctx context.Context, request ai.Request, update func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
			modelCalls++
			historyLengths = append(historyLengths, len(request.Messages))
			if len(request.Tools) != 1 || request.Tools[0].Name != "read" {
				t.Fatalf("missing tool declaration: %+v", request.Tools)
			}
			if modelCalls == 1 {
				partial := answer("temporary")
				partial.StopReason = ai.StopReasonPending
				update(partial)
				if len(executed) != 0 {
					t.Fatal("stream update executed a tool")
				}
				streamReturned = true
				second := call("b", "read")
				return ai.AssistantMessage{StopReason: ai.StopReasonToolUse, Content: []ai.ContentBlock{
					ai.ThinkingContent{Text: "inspect", Signature: "opaque"}, call("a", "read"), &second,
				}}, nil
			}
			if request.Messages[1].(ai.AssistantMessage).Content[0].(ai.ThinkingContent).Signature != "opaque" {
				t.Fatal("reasoning replay data was lost")
			}
			for i, id := range []string{"a", "b"} {
				result := request.Messages[i+2].(ai.ToolResultMessage)
				if result.ToolCallID != id || result.ToolName != "read" || result.IsError {
					t.Fatalf("incorrect tool result: %+v", result)
				}
			}
			return answer("done"), nil
		},
		OnEvent: func(event agent.Event) { events = append(events, event.Type) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 5 || !reflect.DeepEqual(historyLengths, []int{1, 4}) || !reflect.DeepEqual(executed, []string{"a", "b"}) {
		t.Fatalf("history=%d requests=%v executed=%v", len(history), historyLengths, executed)
	}
	if original[:cap(original)][1] != nil {
		t.Fatal("Run wrote into the caller's history backing array")
	}
	want := []agent.EventType{
		agent.EventAgentStart, agent.EventTurnStart, agent.EventMessageStart, agent.EventMessageUpdate, agent.EventMessageEnd,
		agent.EventToolExecutionStart, agent.EventToolExecutionUpdate, agent.EventToolExecutionEnd, agent.EventMessageStart, agent.EventMessageEnd,
		agent.EventToolExecutionStart, agent.EventToolExecutionUpdate, agent.EventToolExecutionEnd, agent.EventMessageStart, agent.EventMessageEnd,
		agent.EventTurnEnd, agent.EventTurnStart, agent.EventMessageStart, agent.EventMessageEnd, agent.EventTurnEnd, agent.EventAgentEnd,
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("event order:\n got %v\nwant %v", events, want)
	}
}

func TestRunToolFailuresBecomeModelInput(t *testing.T) {
	for _, scenario := range []string{"unknown", "invalid-json", "array", "null", "schema", "execution", "truncated"} {
		t.Run(scenario, func(t *testing.T) {
			c := call("a", "read")
			stop := ai.StopReasonToolUse
			executions, requests := 0, 0
			tool := agent.Tool{
				Tool: declaration("read"),
				Execute: func(context.Context, ai.ToolCall, func(ai.ToolResultMessage)) (ai.ToolResultMessage, error) {
					executions++
					return ai.ToolResultMessage{}, errors.New("file not found")
				},
			}
			switch scenario {
			case "unknown":
				c.Name = "missing"
			case "invalid-json":
				c.Arguments = json.RawMessage(`{"path":`)
			case "array":
				c.Arguments = json.RawMessage(`[]`)
			case "null":
				c.Arguments = json.RawMessage(`null`)
			case "schema":
				tool.Validate = func(json.RawMessage) error { return errors.New("path is required") }
			case "truncated":
				// Even valid-looking arguments must not execute after a length stop.
				stop = ai.StopReasonLength
			}
			history, err := agent.Run(context.Background(), []ai.Message{user("read")}, agent.Options{
				Tools: []agent.Tool{tool},
				Stream: func(_ context.Context, request ai.Request, _ func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
					requests++
					if requests == 1 {
						return ai.AssistantMessage{StopReason: stop, Content: []ai.ContentBlock{c}}, nil
					}
					result := request.Messages[2].(ai.ToolResultMessage)
					if !result.IsError || result.ToolCallID != c.ID || len(result.Content) == 0 {
						t.Fatalf("missing error feedback: %+v", result)
					}
					return answer("recovered"), nil
				},
			})
			wantExecutions := 0
			if scenario == "execution" {
				wantExecutions = 1
			}
			if err != nil || executions != wantExecutions || requests != 2 || len(history) != 4 {
				t.Fatalf("err=%v executions=%d requests=%d history=%d", err, executions, requests, len(history))
			}
		})
	}
}

func TestRunCancellationFinishesToolResultsWithoutFurtherEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executions, requests := 0, 0
	var lastEvent agent.Event
	history, err := agent.Run(ctx, []ai.Message{user("work")}, agent.Options{
		Tools: []agent.Tool{{
			Tool: declaration("write"),
			Execute: func(context.Context, ai.ToolCall, func(ai.ToolResultMessage)) (ai.ToolResultMessage, error) {
				executions++
				cancel()
				// The first side effect completed despite concurrent cancellation.
				return ai.ToolResultMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "saved"}}}, nil
			},
		}},
		Stream: func(context.Context, ai.Request, func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
			requests++
			return ai.AssistantMessage{StopReason: ai.StopReasonToolUse, Content: []ai.ContentBlock{call("a", "write"), call("b", "write")}}, nil
		},
		OnEvent: func(event agent.Event) { lastEvent = event },
	})
	if !errors.Is(err, context.Canceled) || executions != 1 || requests != 1 || len(history) != 4 {
		t.Fatalf("err=%v executions=%d requests=%d history=%d", err, executions, requests, len(history))
	}
	if history[2].(ai.ToolResultMessage).IsError || !history[3].(ai.ToolResultMessage).IsError {
		t.Fatal("completed effect or skipped call reported incorrectly")
	}
	if lastEvent.Type != agent.EventAgentEnd || !errors.Is(lastEvent.Err, context.Canceled) {
		t.Fatalf("incorrect final event: %+v", lastEvent)
	}
}

func TestRunModelFailuresPreservePartialResponse(t *testing.T) {
	transportErr := errors.New("connection lost")
	for _, tc := range []struct {
		name string
		stop ai.StopReason
		err  error
	}{
		{"transport", ai.StopReasonPending, transportErr},
		{"cancel", ai.StopReasonPending, context.Canceled},
		{"deadline", ai.StopReasonPending, context.DeadlineExceeded},
		{"provider-error", ai.StopReasonError, nil},
		{"provider-aborted", ai.StopReasonAborted, nil},
		{"unfinished", ai.StopReasonPending, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []agent.EventType
			history, err := agent.Run(context.Background(), []ai.Message{user("work")}, agent.Options{
				Tools: []agent.Tool{{Tool: declaration("write"), Execute: func(context.Context, ai.ToolCall, func(ai.ToolResultMessage)) (ai.ToolResultMessage, error) {
					t.Fatal("executed tool from failed model response")
					return ai.ToolResultMessage{}, nil
				}}},
				Stream: func(context.Context, ai.Request, func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
					return ai.AssistantMessage{StopReason: tc.stop, Content: []ai.ContentBlock{ai.TextContent{Text: "partial"}, call("a", "write")}}, tc.err
				},
				OnEvent: func(event agent.Event) { events = append(events, event.Type) },
			})
			if err == nil || len(history) != 2 {
				t.Fatalf("err=%v history=%d", err, len(history))
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("lost error identity: %v", err)
			}
			if history[1].(ai.AssistantMessage).Content[0].(ai.TextContent).Text != "partial" {
				t.Fatal("partial response lost")
			}
			want := []agent.EventType{agent.EventAgentStart, agent.EventTurnStart, agent.EventMessageStart, agent.EventMessageEnd, agent.EventTurnEnd, agent.EventAgentEnd}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("unbalanced failure events: %v", events)
			}
		})
	}
}

func TestRunMaxTurnsCanResumeWithoutReplayingTools(t *testing.T) {
	executions, requests := 0, 0
	options := agent.Options{
		MaxTurns: 1,
		Tools: []agent.Tool{{Tool: declaration("write"), Execute: func(context.Context, ai.ToolCall, func(ai.ToolResultMessage)) (ai.ToolResultMessage, error) {
			executions++
			return ai.ToolResultMessage{}, nil
		}}},
		Stream: func(_ context.Context, request ai.Request, _ func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
			requests++
			if requests == 1 {
				return ai.AssistantMessage{StopReason: ai.StopReasonToolUse, Content: []ai.ContentBlock{call("a", "write")}}, nil
			}
			if request.Messages[len(request.Messages)-1].Role() != ai.RoleToolResult {
				t.Fatal("resume lost tool result")
			}
			return answer("done"), nil
		},
	}
	history, err := agent.Run(context.Background(), []ai.Message{user("write")}, options)
	if !errors.Is(err, agent.ErrMaxTurns) || len(history) != 3 {
		t.Fatalf("err=%v history=%d", err, len(history))
	}
	history, err = agent.Run(context.Background(), history, options)
	if err != nil || executions != 1 || requests != 2 || len(history) != 4 {
		t.Fatalf("err=%v executions=%d requests=%d history=%d", err, executions, requests, len(history))
	}
}

func TestRunConversionSteeringAndFollowUp(t *testing.T) {
	executions, requests, conversions, steeringPolls, followPolls := 0, 0, 0, 0, 0
	history, err := agent.Run(context.Background(), []ai.Message{
		user("start"), commandExecutionMessage{Command: "pwd", Output: "/workspace"},
	}, agent.Options{
		Tools: []agent.Tool{{Tool: declaration("read"), Execute: func(context.Context, ai.ToolCall, func(ai.ToolResultMessage)) (ai.ToolResultMessage, error) {
			executions++
			return ai.ToolResultMessage{}, nil
		}}},
		ConvertMessages: func(ctx context.Context, messages []ai.Message) ([]ai.Message, error) {
			conversions++
			for i, message := range messages {
				if custom, ok := message.(commandExecutionMessage); ok {
					messages[i] = user(custom.Output)
				}
			}
			return messages, nil
		},
		GetSteering: func(context.Context) ([]ai.Message, error) {
			steeringPolls++
			if steeringPolls == 2 {
				if executions != 2 {
					t.Fatal("steering polled before completing tool batch")
				}
				return []ai.Message{user("change direction")}, nil
			}
			return nil, nil
		},
		GetFollowUp: func(context.Context) ([]ai.Message, error) {
			followPolls++
			if requests < 2 {
				t.Fatal("follow-up polled before natural stop")
			}
			if followPolls == 1 {
				return []ai.Message{user("one more thing")}, nil
			}
			return nil, nil
		},
		Stream: func(_ context.Context, request ai.Request, _ func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
			requests++
			if requests == 1 {
				return ai.AssistantMessage{StopReason: ai.StopReasonToolUse, Content: []ai.ContentBlock{call("a", "read"), call("b", "read")}}, nil
			}
			last := request.Messages[len(request.Messages)-1].(ai.UserMessage).Content[0].(ai.TextContent).Text
			want := "change direction"
			if requests == 3 {
				want = "one more thing"
			}
			if last != want {
				t.Fatalf("last message=%q want=%q", last, want)
			}
			return answer("done"), nil
		},
	})
	if err != nil || requests != 3 || conversions != 3 || len(history) != 9 {
		t.Fatalf("err=%v requests=%d conversions=%d history=%d", err, requests, conversions, len(history))
	}
	if _, ok := history[1].(commandExecutionMessage); !ok {
		t.Fatal("conversion changed stored custom message")
	}
}

// Claiming a built-in role must not bypass concrete-message validation.
type spoofedUser struct{}

func (spoofedUser) Role() ai.Role { return ai.RoleUser }

func TestRunRejectsInvalidInputBeforeModelCall(t *testing.T) {
	for _, messages := range [][]ai.Message{
		nil,
		{nil},
		{(*ai.UserMessage)(nil)},
		{spoofedUser{}},
		{commandExecutionMessage{}},
		{ai.SystemMessage{Content: "system only"}},
		{answer("already done")},
	} {
		t.Run(fmt.Sprintf("%T-%d", last(messages), len(messages)), func(t *testing.T) {
			_, err := agent.Run(context.Background(), messages, agent.Options{
				Stream: func(context.Context, ai.Request, func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
					t.Fatal("called model with invalid input")
					return ai.AssistantMessage{}, nil
				},
			})
			if err == nil {
				t.Fatal("expected invalid-input error")
			}
		})
	}
}

func last(messages []ai.Message) ai.Message {
	if len(messages) == 0 {
		return nil
	}
	return messages[len(messages)-1]
}

func TestRunRejectsAmbiguousToolIDsBeforeAnyEffects(t *testing.T) {
	for _, id := range []string{"", "a"} {
		t.Run("second-id-"+id, func(t *testing.T) {
			_, err := agent.Run(context.Background(), []ai.Message{user("work")}, agent.Options{
				Tools: []agent.Tool{{Tool: declaration("write"), Execute: func(context.Context, ai.ToolCall, func(ai.ToolResultMessage)) (ai.ToolResultMessage, error) {
					t.Fatal("executed ambiguous tool batch")
					return ai.ToolResultMessage{}, nil
				}}},
				Stream: func(context.Context, ai.Request, func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
					return ai.AssistantMessage{StopReason: ai.StopReasonToolUse, Content: []ai.ContentBlock{call("a", "write"), call(id, "write")}}, nil
				},
			})
			if err == nil {
				t.Fatal("expected ambiguous-call error")
			}
		})
	}
}

func TestRunCancellationAtTurnEndWinsOverLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := agent.Run(ctx, []ai.Message{user("hello")}, agent.Options{
		MaxTurns: 1,
		Stream: func(context.Context, ai.Request, func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
			return answer("done"), nil
		},
		OnEvent: func(event agent.Event) {
			if event.Type == agent.EventTurnEnd {
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was swallowed: %v", err)
	}
}

func TestRunPreservesDrainedMessagesOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []agent.EventType
	history, err := agent.Run(ctx, []ai.Message{user("first")}, agent.Options{
		GetSteering: func(context.Context) ([]ai.Message, error) {
			cancel()
			return []ai.Message{user("queued")}, nil
		},
		Stream: func(context.Context, ai.Request, func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
			t.Fatal("model called after cancellation")
			return ai.AssistantMessage{}, nil
		},
		OnEvent: func(event agent.Event) { events = append(events, event.Type) },
	})
	if !errors.Is(err, context.Canceled) || len(history) != 2 {
		t.Fatalf("err=%v history=%d", err, len(history))
	}
	want := []agent.EventType{agent.EventAgentStart, agent.EventTurnStart, agent.EventMessageStart, agent.EventMessageEnd, agent.EventTurnEnd, agent.EventAgentEnd}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("incorrect queue lifecycle: %v", events)
	}
}

func TestRunStopsBeforeSideEffectsWhenValidationCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	history, err := agent.Run(ctx, []ai.Message{user("write")}, agent.Options{
		Tools: []agent.Tool{{
			Tool: declaration("write"),
			Validate: func(json.RawMessage) error {
				cancel()
				return nil
			},
			Execute: func(context.Context, ai.ToolCall, func(ai.ToolResultMessage)) (ai.ToolResultMessage, error) {
				t.Fatal("executed after validator canceled context")
				return ai.ToolResultMessage{}, nil
			},
		}},
		Stream: func(context.Context, ai.Request, func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
			return ai.AssistantMessage{StopReason: ai.StopReasonToolUse, Content: []ai.ContentBlock{call("a", "write")}}, nil
		},
	})
	if !errors.Is(err, context.Canceled) || len(history) != 3 || !history[2].(ai.ToolResultMessage).IsError {
		t.Fatalf("err=%v history=%v", err, history)
	}
}

func TestRunConversionFailureDoesNotCallModel(t *testing.T) {
	convertErr := errors.New("cannot render custom message")
	history, err := agent.Run(context.Background(), []ai.Message{commandExecutionMessage{}}, agent.Options{
		ConvertMessages: func(context.Context, []ai.Message) ([]ai.Message, error) {
			return nil, convertErr
		},
		Stream: func(context.Context, ai.Request, func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
			t.Fatal("called model after conversion error")
			return ai.AssistantMessage{}, nil
		},
	})
	if !errors.Is(err, convertErr) || len(history) != 1 {
		t.Fatalf("err=%v history=%d", err, len(history))
	}
}

func TestRunLimitDoesNotDrainQueues(t *testing.T) {
	steeringPolls := 0
	_, err := agent.Run(context.Background(), []ai.Message{user("hello")}, agent.Options{
		MaxTurns: 1,
		GetSteering: func(context.Context) ([]ai.Message, error) {
			steeringPolls++
			if steeringPolls > 1 {
				t.Fatal("drained steering after reaching limit")
			}
			return nil, nil
		},
		GetFollowUp: func(context.Context) ([]ai.Message, error) {
			t.Fatal("drained follow-up after reaching limit")
			return nil, nil
		},
		Stream: func(context.Context, ai.Request, func(ai.AssistantMessage)) (ai.AssistantMessage, error) {
			return answer("done"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}
