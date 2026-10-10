package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/bitinit/pigo/agent"
	"github.com/bitinit/pigo/ai"
	"github.com/bitinit/pigo/ai/deepseek"
	"github.com/bitinit/pigo/codingagent"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	stopInput := context.AfterFunc(ctx, func() { _ = os.Stdin.Close() })
	defer stopInput()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("pigo", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	directory := flags.String("cwd", ".", "Working directory")
	modelDefault := os.Getenv("DEEPSEEK_MODEL")
	if modelDefault == "" {
		modelDefault = "deepseek-flash"
	}
	model := flags.String("model", modelDefault, "DeepSeek model ID")
	baseURL := flags.String("base-url", os.Getenv("DEEPSEEK_BASE_URL"), "DeepSeek API base URL")
	thinking := flags.String("thinking", "", "Thinking mode: enabled or disabled; empty uses the provider default")
	sessionPath := flags.String("session", "", "Optional session JSON file to load and save")
	maxTurns := flags.Int("max-turns", 40, "Maximum model calls per prompt")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	client, err := deepseek.New(deepseek.Config{APIKey: os.Getenv("DEEPSEEK_API_KEY"), Model: *model, BaseURL: *baseURL, Thinking: *thinking})
	if err != nil {
		return err
	}
	printed := ""
	session, err := codingagent.New(codingagent.Options{Directory: *directory, Agent: agent.Options{
		Stream: client.Stream, MaxTurns: *maxTurns,
		OnEvent: func(event agent.Event) {
			if event.Type == agent.EventToolExecutionStart {
				fmt.Fprintf(diagnostics, "[%s]\n", event.Call.Name)
			}
			message, ok := event.Message.(ai.AssistantMessage)
			if !ok {
				return
			}
			if event.Type == agent.EventMessageStart {
				printed = ""
			}
			if event.Type != agent.EventMessageUpdate && event.Type != agent.EventMessageEnd {
				return
			}
			var text strings.Builder
			for _, block := range message.Content {
				if block, ok := block.(ai.TextContent); ok {
					text.WriteString(block.Text)
				}
			}
			current := text.String()
			if strings.HasPrefix(current, printed) {
				fmt.Fprint(output, current[len(printed):])
			}
			printed = current
			if event.Type == agent.EventMessageEnd && current != "" {
				fmt.Fprintln(output)
			}
		},
	}})
	if err != nil {
		return err
	}
	if *sessionPath != "" {
		if err := session.Load(*sessionPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	act := func(text string) error {
		var runErr error
		switch text {
		case "/reset":
			runErr = session.Agent.Reset()
		case "/continue":
			runErr = session.Agent.Continue(ctx)
		default:
			runErr = session.Agent.Prompt(ctx, ai.UserMessage{Content: []ai.ContentBlock{ai.TextContent{Text: text}}})
		}
		if *sessionPath != "" {
			if saveErr := session.Save(*sessionPath); saveErr != nil {
				return errors.Join(runErr, fmt.Errorf("save session: %w", saveErr))
			}
		}
		return runErr
	}
	if flags.NArg() > 0 {
		return act(strings.Join(flags.Args(), " "))
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	fmt.Fprintln(diagnostics, "pigo: /continue, /reset, /exit; Ctrl-C cancels and exits")
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "/exit" {
			return nil
		}
		if text == "" {
			continue
		}
		if err := act(text); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Fprintln(diagnostics, err)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return scanner.Err()
}
