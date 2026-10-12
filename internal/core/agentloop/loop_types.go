package agentloop

import (
	"context"
	"encoding/json"
	"github.com/bitinit/pigo/internal/core/ai"
)

// Tool combines a model-visible declaration with its Go implementation.
type Tool struct {
	ai.Tool
	// Validate optionally checks arguments against the declared schema or domain
	// rules. The loop itself checks only that Arguments is a JSON object.
	Validate func(json.RawMessage) error
	// Execute receives a complete call. The loop sets ToolCallID and ToolName on
	// returned results. An ordinary error becomes an error result for the model;
	// context cancellation ends the run. Execute must honor ctx.
	// Updates must be synchronous and must stop before Execute returns.
	Execute func(ctx context.Context, call ai.ToolCall, update func(ai.ToolResultMessage)) (ai.ToolResultMessage, error)
}

// Options configures one Run. Functions are called synchronously: no goroutines,
// channels, retries, or concurrent tool execution are created by the loop.
// Messages and callback payloads are read-only borrowed data. Copy payloads if
// retaining them while their producer may still be updating them.
type Options struct {
	// Stream returns the authoritative final response, including partial content
	// on error. update receives assembled assistant snapshots, not JSON fragments.
	// Stream must honor ctx, call update serially, and stop updates before returning.
	// Model settings and credentials can be captured in the supplied function.
	Stream func(ctx context.Context, request ai.Request, update func(ai.AssistantMessage)) (ai.AssistantMessage, error)
	Tools  []Tool
	// ConvertMessages runs before every model call without changing stored history.
	// Nil passes through built-in ai messages and rejects custom message types.
	ConvertMessages MessageConverter
	// OnEvent runs synchronously and provides backpressure. It must not mutate
	// payloads. Cancellation is requested through the context supplied to Run.
	OnEvent func(Event)
	// MaxTurns limits model calls, not tool calls. Zero means unlimited.
	MaxTurns int
	// GetSteering drains messages before the first call and after completed tool
	// batches. It does not interrupt tools. GetFollowUp is polled only when the
	// loop would otherwise stop. Both return nil when their caller-owned queue is
	// empty; they must not block waiting for new input.
	// Queues are not polled again once MaxTurns is reached.
	GetSteering func(context.Context) ([]ai.Message, error)
	GetFollowUp func(context.Context) ([]ai.Message, error)
}

type EventType string

const (
	EventAgentStart          EventType = "agent_start"
	EventAgentEnd            EventType = "agent_end"
	EventTurnStart           EventType = "turn_start"
	EventTurnEnd             EventType = "turn_end"
	EventMessageStart        EventType = "message_start"
	EventMessageUpdate       EventType = "message_update"
	EventMessageEnd          EventType = "message_end"
	EventToolExecutionStart  EventType = "tool_execution_start"
	EventToolExecutionUpdate EventType = "tool_execution_update"
	EventToolExecutionEnd    EventType = "tool_execution_end"
)

// Event reports progress. Turn counts model turns from one (zero at agent_start).
// Message carries message events, tool progress/results, or the assistant at
// turn_end. Call is set for tool events. Err reports execution or run failures.
// The returned history, rather than progress events, is the final run result.
type Event struct {
	Type    EventType
	Turn    int
	Message ai.Message
	Call    *ai.ToolCall
	Err     error
}
