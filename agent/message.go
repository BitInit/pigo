// Package agent defines the contracts for extensible agent conversations.
package agent

import (
	"context"

	"github.com/bitinit/pigo/ai"
)

// MessageConverter prepares model input from agent history. Implementations may
// pass through supported ai messages, translate custom messages, or deliberately
// omit UI-only messages. They must not modify the supplied history, must honor
// context cancellation, and must return an error for unsupported input unless
// an explicit application policy permits filtering it.
//
// A role string alone does not establish the concrete type. Converters and
// providers must not assume arbitrary implementations are built-in ai messages.
type MessageConverter func(ctx context.Context, messages []ai.Message) ([]ai.Message, error)
