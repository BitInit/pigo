package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bitinit/pigo/ai"
)

// ErrMaxTurns means another tool-response turn was needed but the limit was hit.
// The returned history includes all completed tool results and can be resumed.
var ErrMaxTurns = errors.New("agent: maximum model turns reached")

// Run continues a conversation until the model stops requesting tools and there
// are no queued messages. To start a new prompt, append it to history before Run.
// To continue after completed tool results, pass the returned history unchanged.
// The model-facing transcript must end in a user or tool-result message.
//
// Run returns the full updated history even on error. It owns a new history slice
// but shares existing message payloads, which callers and callbacks must treat as
// read-only. It never replays tool calls from history or retries model requests.
// Cancellation cannot roll back a tool's external side effects.
func Run(ctx context.Context, messages []ai.Message, options Options) (history []ai.Message, err error) {
	history = append([]ai.Message(nil), messages...)
	if options.Stream == nil {
		return history, errors.New("agent: Stream is required")
	}
	if options.MaxTurns < 0 {
		return history, errors.New("agent: MaxTurns must not be negative")
	}
	tools := make(map[string]Tool, len(options.Tools))
	declarations := make([]ai.Tool, 0, len(options.Tools))
	for _, tool := range options.Tools {
		if tool.Name == "" || tool.Execute == nil {
			return history, errors.New("agent: tools require a name and Execute function")
		}
		if _, exists := tools[tool.Name]; exists {
			return history, fmt.Errorf("agent: duplicate tool %q", tool.Name)
		}
		if !jsonObject(tool.Parameters) {
			return history, fmt.Errorf("agent: tool %q parameters must be a JSON schema object", tool.Name)
		}
		tools[tool.Name] = tool
		declarations = append(declarations, tool.Tool)
	}
	turn := 0
	emit := func(event Event) {
		event.Turn = turn
		if options.OnEvent != nil {
			options.OnEvent(event)
		}
	}
	appendMessage := func(message ai.Message) {
		history = append(history, message)
		emit(Event{Type: EventMessageStart, Message: message})
		emit(Event{Type: EventMessageEnd, Message: message})
	}
	emit(Event{Type: EventAgentStart})
	defer func() { emit(Event{Type: EventAgentEnd, Err: err}) }()
	if err := ctx.Err(); err != nil {
		return history, err
	}
	var pending []ai.Message
	if options.GetSteering != nil {
		pending, err = options.GetSteering(ctx)
		if err != nil {
			return history, fmt.Errorf("agent: steering: %w", err)
		}
	}
	for {
		turn++
		emit(Event{Type: EventTurnStart})
		// Keep already-drained messages even if cancellation arrived while draining.
		for _, message := range pending {
			appendMessage(message)
		}
		pending = nil
		if err := ctx.Err(); err != nil {
			emit(Event{Type: EventTurnEnd, Err: err})
			return history, err
		}
		requestMessages := append([]ai.Message(nil), history...)
		if options.ConvertMessages != nil {
			requestMessages, err = options.ConvertMessages(ctx, requestMessages)
		}
		if err == nil {
			err = validateModelMessages(requestMessages)
		}
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			emit(Event{Type: EventTurnEnd, Err: err})
			return history, err
		}
		started := false
		response, streamErr := options.Stream(ctx, ai.Request{
			Messages: requestMessages,
			Tools:    append([]ai.Tool(nil), declarations...),
		}, func(partial ai.AssistantMessage) {
			if !started {
				started = true
				emit(Event{Type: EventMessageStart, Message: partial})
			}
			emit(Event{Type: EventMessageUpdate, Message: partial})
		})
		if response.Timestamp == 0 {
			response.Timestamp = time.Now().UnixMilli()
		}
		if ctx.Err() != nil {
			streamErr = ctx.Err()
		}
		if streamErr != nil {
			response.StopReason = ai.StopReasonError
			if errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, context.DeadlineExceeded) {
				response.StopReason = ai.StopReasonAborted
			}
			response.ErrorMessage = streamErr.Error()
		} else {
			switch response.StopReason {
			case ai.StopReasonStop, ai.StopReasonToolUse, ai.StopReasonLength:
			case ai.StopReasonAborted:
				streamErr = context.Canceled
			case ai.StopReasonError:
				streamErr = fmt.Errorf("agent: model failed: %s", response.ErrorMessage)
			default:
				streamErr = fmt.Errorf("agent: model returned non-final stop reason %q", response.StopReason)
			}
		}
		history = append(history, response)
		if !started {
			emit(Event{Type: EventMessageStart, Message: response})
		}
		emit(Event{Type: EventMessageEnd, Message: response})
		if streamErr != nil {
			emit(Event{Type: EventTurnEnd, Message: response, Err: streamErr})
			return history, streamErr
		}
		calls, callErr := responseToolCalls(response)
		if callErr != nil {
			emit(Event{Type: EventTurnEnd, Message: response, Err: callErr})
			return history, callErr
		}
		var runErr error
		for _, call := range calls {
			emit(Event{Type: EventToolExecutionStart, Call: &call})
			result, toolErr := executeTool(ctx, tools, call, response.StopReason, runErr, func(partial ai.ToolResultMessage) {
				partial.ToolCallID, partial.ToolName = call.ID, call.Name
				emit(Event{Type: EventToolExecutionUpdate, Call: &call, Message: partial})
			})
			if toolErr != nil {
				result.IsError = true
				result.Content = append(result.Content, ai.TextContent{Text: toolErr.Error()})
			}
			result.ToolCallID, result.ToolName = call.ID, call.Name
			if result.Timestamp == 0 {
				result.Timestamp = time.Now().UnixMilli()
			}
			emit(Event{Type: EventToolExecutionEnd, Call: &call, Message: result, Err: toolErr})
			appendMessage(result)
			if ctx.Err() != nil {
				runErr = ctx.Err()
			} else if errors.Is(toolErr, context.Canceled) || errors.Is(toolErr, context.DeadlineExceeded) {
				runErr = toolErr
			}
			// Remaining calls receive cancellation results without being executed.
		}
		if runErr == nil {
			runErr = ctx.Err()
		}
		emit(Event{Type: EventTurnEnd, Message: response, Err: runErr})
		if runErr != nil {
			return history, runErr
		}
		if err := ctx.Err(); err != nil {
			return history, err
		}
		if options.MaxTurns > 0 && turn >= options.MaxTurns {
			if len(calls) > 0 {
				return history, ErrMaxTurns
			}
			return history, nil
		}
		if options.GetSteering != nil {
			pending, err = options.GetSteering(ctx)
			if err != nil {
				return history, fmt.Errorf("agent: steering: %w", err)
			}
		}
		if len(calls) > 0 || len(pending) > 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return history, err
		}
		if options.GetFollowUp != nil {
			pending, err = options.GetFollowUp(ctx)
			if err != nil {
				return history, fmt.Errorf("agent: follow-up: %w", err)
			}
		}
		if len(pending) == 0 {
			return history, ctx.Err()
		}
	}
}

func validateModelMessages(messages []ai.Message) error {
	if len(messages) == 0 {
		return errors.New("agent: no model messages")
	}
	for i, message := range messages {
		valid := true
		switch m := message.(type) {
		case ai.SystemMessage, ai.UserMessage, ai.AssistantMessage, ai.ToolResultMessage:
		case *ai.SystemMessage:
			valid = m != nil
		case *ai.UserMessage:
			valid = m != nil
		case *ai.AssistantMessage:
			valid = m != nil
		case *ai.ToolResultMessage:
			valid = m != nil
		default:
			valid = false
		}
		if !valid {
			return fmt.Errorf("agent: unsupported or nil model message at index %d (%T); convert custom messages before calling the model", i, message)
		}
	}
	role := messages[len(messages)-1].Role()
	if role != ai.RoleUser && role != ai.RoleToolResult {
		return fmt.Errorf("agent: model input must end with user or toolResult, got %q", role)
	}
	return nil
}

func responseToolCalls(response ai.AssistantMessage) ([]ai.ToolCall, error) {
	var calls []ai.ToolCall
	ids := make(map[string]bool)
	for _, block := range response.Content {
		var call ai.ToolCall
		switch block := block.(type) {
		case ai.TextContent, ai.ThinkingContent:
			continue
		case *ai.TextContent:
			if block != nil {
				continue
			}
			return nil, errors.New("agent: nil text block")
		case *ai.ThinkingContent:
			if block != nil {
				continue
			}
			return nil, errors.New("agent: nil thinking block")
		case ai.ToolCall:
			call = block
		case *ai.ToolCall:
			if block == nil {
				return nil, errors.New("agent: nil tool call")
			}
			call = *block
		default:
			return nil, fmt.Errorf("agent: unsupported assistant content %T", block)
		}
		if call.ID == "" || ids[call.ID] {
			return nil, fmt.Errorf("agent: missing or duplicate tool call id %q", call.ID)
		}
		ids[call.ID] = true
		calls = append(calls, call)
	}
	if response.StopReason == ai.StopReasonToolUse && len(calls) == 0 {
		return nil, errors.New("agent: toolUse response contains no tool calls")
	}
	return calls, nil
}

func executeTool(ctx context.Context, tools map[string]Tool, call ai.ToolCall, stop ai.StopReason, interrupted error, update func(ai.ToolResultMessage)) (ai.ToolResultMessage, error) {
	if err := ctx.Err(); err != nil {
		return ai.ToolResultMessage{}, err
	}
	if interrupted != nil {
		return ai.ToolResultMessage{}, interrupted
	}
	if stop == ai.StopReasonLength {
		return ai.ToolResultMessage{}, errors.New("tool not executed: model output was truncated; reissue the call with complete arguments")
	}
	tool, ok := tools[call.Name]
	if !ok {
		return ai.ToolResultMessage{}, fmt.Errorf("unknown tool %q", call.Name)
	}
	if !jsonObject(call.Arguments) {
		return ai.ToolResultMessage{}, errors.New("tool arguments must be a complete JSON object")
	}
	if tool.Validate != nil {
		if err := tool.Validate(call.Arguments); err != nil {
			return ai.ToolResultMessage{}, fmt.Errorf("invalid arguments for %q: %w", call.Name, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return ai.ToolResultMessage{}, err
	}
	return tool.Execute(ctx, call, update)
}

func jsonObject(data json.RawMessage) bool {
	data = bytes.TrimSpace(data)
	return len(data) > 0 && data[0] == '{' && json.Valid(data)
}
