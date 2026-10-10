// Package deepseek implements DeepSeek's streaming Chat Completions API.
package deepseek

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bitinit/pigo/ai"
)

// Config selects one model and transport. The client never reads process credentials.
type Config struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
	MaxTokens  int
	// Thinking is "enabled", "disabled", or empty to use the provider default.
	Thinking        string
	ReasoningEffort string
}

// Client is safe for concurrent independent requests. It does not retry requests.
type Client struct{ config Config }

// New validates configuration without making a network request.
func New(config Config) (*Client, error) {
	if config.APIKey == "" || config.Model == "" {
		return nil, errors.New("deepseek: APIKey and Model are required")
	}
	if config.BaseURL == "" {
		config.BaseURL = "https://api.deepseek.com"
	}
	u, err := url.Parse(config.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("deepseek: invalid BaseURL")
	}
	if config.Thinking != "" && config.Thinking != "enabled" && config.Thinking != "disabled" {
		return nil, errors.New("deepseek: invalid thinking mode")
	}
	if config.MaxTokens < 0 {
		return nil, errors.New("deepseek: MaxTokens cannot be negative")
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 10 * time.Minute}
	}
	return &Client{config: config}, nil
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type toolCall struct {
	Index    int          `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function functionCall `json:"function"`
}
type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Reasoning  string     `json:"reasoning_content,omitempty"`
	Calls      []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type tool struct {
	Type     string  `json:"type"`
	Function ai.Tool `json:"function"`
}
type request struct {
	Model         string    `json:"model"`
	Messages      []message `json:"messages"`
	Tools         []tool    `json:"tools,omitempty"`
	Stream        bool      `json:"stream"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	MaxTokens int `json:"max_tokens,omitempty"`
	Thinking  *struct {
		Type string `json:"type"`
	} `json:"thinking,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// Stream implements agent.Options.Stream. Updates are independent assembled
// snapshots. Partial tool arguments are never executed here. Text and tool calls
// are supported; unsupported content (including images) is rejected explicitly.
func (c *Client) Stream(ctx context.Context, input ai.Request, update func(ai.AssistantMessage)) (result ai.AssistantMessage, err error) {
	result = ai.AssistantMessage{Provider: "deepseek", Model: c.config.Model, StopReason: ai.StopReasonPending, Timestamp: time.Now().UnixMilli()}
	defer func() {
		if err != nil {
			result.StopReason, result.ErrorMessage = ai.StopReasonError, err.Error()
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				result.StopReason = ai.StopReasonAborted
			}
		}
	}()
	body := request{Model: c.config.Model, Stream: true, MaxTokens: c.config.MaxTokens, ReasoningEffort: c.config.ReasoningEffort}
	body.StreamOptions.IncludeUsage = true
	if c.config.Thinking != "" {
		body.Thinking = &struct {
			Type string `json:"type"`
		}{c.config.Thinking}
	}
	for _, item := range input.Messages {
		m, include, encodeErr := encodeMessage(item)
		if encodeErr != nil {
			return result, encodeErr
		}
		if include {
			body.Messages = append(body.Messages, m)
		}
	}
	if len(body.Messages) == 0 {
		return result, errors.New("deepseek: empty transcript")
	}
	for _, t := range input.Tools {
		body.Tools = append(body.Tools, tool{Type: "function", Function: t})
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.config.BaseURL, "/")+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		return result, fmt.Errorf("deepseek: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return result, errors.New("deepseek: expected an SSE response")
	}
	positions := map[int]int{}
	finished, done := false, false
	consumed := 0
	process := func(data string) error {
		if data == "[DONE]" {
			done = true
			return nil
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Content   string     `json:"content"`
					Reasoning string     `json:"reasoning_content"`
					Calls     []toolCall `json:"tool_calls"`
				} `json:"delta"`
				Finish string `json:"finish_reason"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Usage *struct {
				Input     int64  `json:"prompt_tokens"`
				Output    int64  `json:"completion_tokens"`
				CacheHit  int64  `json:"prompt_cache_hit_tokens"`
				CacheMiss *int64 `json:"prompt_cache_miss_tokens"`
				Total     int64  `json:"total_tokens"`
				Details   *struct {
					Reasoning *int64 `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("deepseek: invalid stream event: %w", err)
		}
		if chunk.Error != nil {
			return fmt.Errorf("deepseek: %s", chunk.Error.Message)
		}
		if chunk.ID != "" {
			result.ResponseID = chunk.ID
		}
		if chunk.Model != "" {
			result.Model = chunk.Model
		}
		if chunk.Usage != nil {
			u := chunk.Usage
			result.Usage = ai.Usage{Input: u.Input - u.CacheHit, Output: u.Output, CacheRead: u.CacheHit, TotalTokens: u.Total}
			if u.CacheMiss != nil {
				result.Usage.Input = *u.CacheMiss
			}
			if u.Details != nil {
				result.Usage.Reasoning = u.Details.Reasoning
			}
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				return errors.New("deepseek: unexpected choice index")
			}
			if finished {
				return errors.New("deepseek: content after final choice")
			}
			appendText(&result, choice.Delta.Reasoning, true)
			appendText(&result, choice.Delta.Content, false)
			for _, delta := range choice.Delta.Calls {
				if delta.Index < 0 || delta.Index >= 128 {
					return errors.New("deepseek: invalid tool call index")
				}
				position, exists := positions[delta.Index]
				if !exists {
					if delta.Index != len(positions) {
						return errors.New("deepseek: tool call indices must begin in order")
					}
					position = len(result.Content)
					positions[delta.Index] = position
					result.Content = append(result.Content, ai.ToolCall{})
				}
				call := result.Content[position].(ai.ToolCall)
				if delta.ID != "" {
					call.ID = delta.ID
				}
				if delta.Type != "" && delta.Type != "function" {
					return errors.New("deepseek: unsupported tool type")
				}
				call.Name += delta.Function.Name
				call.Arguments = append(call.Arguments, delta.Function.Arguments...)
				result.Content[position] = call
			}
			if choice.Finish != "" {
				finished = true
				switch choice.Finish {
				case "stop":
					result.StopReason = ai.StopReasonStop
				case "length":
					result.StopReason = ai.StopReasonLength
				case "tool_calls":
					result.StopReason = ai.StopReasonToolUse
				case "aborted":
					return context.Canceled
				default:
					return fmt.Errorf("deepseek: response stopped: %s", choice.Finish)
				}
			}
		}
		if update != nil {
			update(ai.CloneMessage(result).(ai.AssistantMessage))
		}
		return ctx.Err()
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		consumed += len(line)
		if consumed > 32<<20 {
			return result, errors.New("deepseek: response exceeds 32 MiB")
		}
		if line == "" {
			if len(data) == 0 {
				continue
			}
			err = process(strings.Join(data, "\n"))
			data = nil
			if err != nil {
				return result, err
			}
			if done {
				break
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if !done && len(data) > 0 {
		if err := process(strings.Join(data, "\n")); err != nil {
			return result, err
		}
	}
	if !done || !finished {
		return result, errors.New("deepseek: incomplete SSE response")
	}
	return result, ctx.Err()
}

func appendText(result *ai.AssistantMessage, text string, thinking bool) {
	if text == "" {
		return
	}
	last := len(result.Content) - 1
	if last >= 0 {
		if thinking {
			if b, ok := result.Content[last].(ai.ThinkingContent); ok {
				b.Text += text
				result.Content[last] = b
				return
			}
		}
		if !thinking {
			if b, ok := result.Content[last].(ai.TextContent); ok {
				b.Text += text
				result.Content[last] = b
				return
			}
		}
	}
	if thinking {
		result.Content = append(result.Content, ai.ThinkingContent{Text: text})
	} else {
		result.Content = append(result.Content, ai.TextContent{Text: text})
	}
}

func encodeMessage(input ai.Message) (message, bool, error) {
	var result message
	var content []ai.ContentBlock
	switch m := ai.CloneMessage(input).(type) {
	case ai.SystemMessage:
		return message{Role: "system", Content: m.Content}, true, nil
	case ai.UserMessage:
		result.Role, content = "user", m.Content
	case ai.ToolResultMessage:
		result.Role, result.ToolCallID, content = "tool", m.ToolCallID, m.Content
	case ai.AssistantMessage:
		// Failed outputs are retained in agent history but are not valid model input.
		if m.StopReason == ai.StopReasonError || m.StopReason == ai.StopReasonAborted {
			return result, false, nil
		}
		result.Role, content = "assistant", m.Content
	default:
		return result, false, fmt.Errorf("deepseek: unsupported message %T", input)
	}
	for _, item := range content {
		switch block := item.(type) {
		case ai.TextContent:
			result.Content += block.Text
		case ai.ThinkingContent:
			if result.Role != "assistant" || block.Redacted {
				return result, false, errors.New("deepseek: unsupported reasoning block")
			}
			result.Reasoning += block.Text
		case ai.ToolCall:
			if result.Role != "assistant" {
				return result, false, errors.New("deepseek: tool call outside assistant message")
			}
			result.Calls = append(result.Calls, toolCall{ID: block.ID, Type: "function", Function: functionCall{Name: block.Name, Arguments: string(block.Arguments)}})
		default:
			return result, false, fmt.Errorf("deepseek: unsupported content %T", item)
		}
	}
	return result, true, nil
}
