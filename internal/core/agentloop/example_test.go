package agentloop_test

import (
	"context"
	"fmt"

	"github.com/bitinit/pigo/internal/core/agentloop"
	"github.com/bitinit/pigo/internal/core/ai"
)

// An application owns its message payload and needs only a Role method.
type commandExecutionMessage struct {
	Command string
	Output  string
}

func (commandExecutionMessage) Role() ai.Role { return "coding.commandExecution" }

func ExampleMessageConverter() {
	user := ai.UserMessage{Content: []ai.ContentBlock{
		ai.TextContent{Text: "Explain the command output"},
	}}
	history := []ai.Message{
		user,
		commandExecutionMessage{Command: "pwd", Output: "/workspace"},
	}

	// This application explicitly supports user messages and command records.
	// A production converter would also handle the other built-in message types.
	var convert agentloop.MessageConverter = func(ctx context.Context, messages []ai.Message) ([]ai.Message, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result := make([]ai.Message, 0, len(messages))
		for _, message := range messages {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			switch message := message.(type) {
			case ai.UserMessage:
				result = append(result, message)
			case commandExecutionMessage:
				result = append(result, ai.UserMessage{Content: []ai.ContentBlock{
					ai.TextContent{Text: message.Command + "\n" + message.Output},
				}})
			default:
				return nil, fmt.Errorf("unsupported message %T", message)
			}
		}
		return result, nil
	}

	messages, err := convert(context.Background(), history)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(history[1].Role())
	fmt.Println(messages[1].Role())
	fmt.Println(messages[1].(ai.UserMessage).Content[0].(ai.TextContent).Text)
	// Output:
	// coding.commandExecution
	// user
	// pwd
	// /workspace
}
