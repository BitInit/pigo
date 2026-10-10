# pigo

A small Go coding-agent harness inspired by pi. The core is usable as a library;
the CLI is a client of the same code. Go 1.22 or later is required. There are no
third-party Go dependencies.

## Packages

- `ai`: open `Message` and `ContentBlock` interfaces, concrete model messages,
  request/tool declarations, token usage, copying, and built-in message codecs.
- `ai/deepseek`: streaming DeepSeek Chat Completions adapter using `net/http`.
- `agent`: the stateless `Run` loop and a stateful `Agent` with one active run,
  conversation history, cancellation, event callbacks, and message queues.
- `codingagent`: coding instructions, project context, four tools, and explicit
  session persistence.
- `cmd/pigo`: a thin terminal client.

`Message` is defined only in `ai` and requires `Role() ai.Role`. Application-defined
messages implement it directly. `ConvertMessages` translates or filters custom
messages before each model request. The default path rejects unsupported types.

## Run

Set `DEEPSEEK_API_KEY` in the environment, then run from the module directory:

```sh
go run ./cmd/pigo -cwd /path/to/project "Inspect the code and explain its structure"
```

To use an interactive conversation with explicit persistence:

```sh
go run ./cmd/pigo -cwd /path/to/project -session ./conversation.json
```

Each non-empty input line is one prompt. Commands:

- `/continue`: continue after a turn limit or process a queued prompt. Previously
  completed tools are never replayed.
- `/reset`: clear the conversation and queues, retaining initial instructions.
- `/exit`: exit the client.
- Ctrl-C: cancel the active operation and exit. If `-session` was provided,
  completed and interrupted results are saved after the run settles.

Flags include `-model`, `-base-url`, `-thinking enabled|disabled`, and `-max-turns`.
The CLI defaults to `deepseek-flash` and 40 model calls per prompt. Set
`DEEPSEEK_MODEL` or `-model` to choose another model. `DEEPSEEK_BASE_URL` or
`-base-url` selects an alternate endpoint. Model IDs are not hardcoded in the
provider adapter. Session paths are relative to the launching process's directory.

The adapter follows the official [Chat Completions API](https://api-docs.deepseek.com/api/create-chat-completion/)
and preserves reasoning content for subsequent requests as described in
[Thinking Mode](https://api-docs.deepseek.com/guides/thinking_mode/).

## VS Code debugging

Open the `pigo` module directory itself in VS Code. The repository recommends the
official Go extension (`golang.go`). If Delve is missing or too old, run
**Go: Install/Update Tools** from the Command Palette and select `dlv`.

Create `.env` from `.env.example` if it does not exist, then set
`DEEPSEEK_API_KEY` in `.env`. Optional model and endpoint settings are also included
in the template. `.env` is ignored by Git. Do not put API keys in `launch.json`.
VS Code loads this file for the CLI profiles; the CLI itself does not load `.env`.

Select a profile in **Run and Debug**, set breakpoints, and press **F5**:

| Profile | Behavior |
| --- | --- |
| `pigo: Interactive CLI` | Start a fresh conversation and type prompts in the integrated terminal |
| `pigo: Single task` | Enter a prompt in a dialog and debug that task |
| `pigo: Current package tests` | Open a Go test file, then debug that package's tests without a real API key |

Use **Run Without Debugging** to launch a selected CLI profile without breakpoints.
Enter messages in the **Terminal**, not the **Debug Console**, which evaluates Go
expressions. The test profile does not load `.env` and uses the existing fake model
and local HTTP tests. Tests involving deadlines can fail if paused too long.

The CLI profiles use the workspace as the coding agent's working directory. To
work on another project, change the value after `-cwd` in the selected profile's
`args`; leave `program` pointing at `cmd/pigo`. Profiles start without a session
file by default. To persist a conversation, add `-session` and an explicit path
before the prompt (and before `--` in the single-task profile).

Useful breakpoint locations include `run` in `cmd/pigo/main.go`, `Run` in
`agent/loop.go`, the tool methods in `codingagent/tools.go`, and `Client.Stream`
in `ai/deepseek/client.go`. **Tasks: Run Task** also offers `go: test` and `go: vet`.
Delve builds the debug binary itself; no separate build task is needed.

See the [Go extension debugging guide](https://github.com/golang/vscode-go/blob/master/docs/debugging.md)
for debugger installation and platform-specific troubleshooting.

## Embed in Go

```go
func runCodingTask(ctx context.Context, directory, apiKey, model string) error {
    client, err := deepseek.New(deepseek.Config{
        APIKey: apiKey,
        Model:  model,
    })
    if err != nil {
        return err
    }

    session, err := codingagent.New(codingagent.Options{
        Directory: directory,
        Agent: agent.Options{
            Stream:   client.Stream,
            MaxTurns: 40,
            OnEvent: func(event agent.Event) {
                // Forward updates to a terminal, desktop client, or server client.
            },
        },
    })
    if err != nil {
        return err
    }

    return session.Agent.Prompt(ctx, ai.UserMessage{
        Content: []ai.ContentBlock{
            ai.TextContent{Text: "Read the failing test and fix the implementation"},
        },
    })
}
```

Import `context`, `github.com/bitinit/pigo/ai`, `github.com/bitinit/pigo/ai/deepseek`,
`github.com/bitinit/pigo/agent`, and `github.com/bitinit/pigo/codingagent` for this example.

`Prompt` and `Continue` run synchronously. A client can start them in its own
goroutine and call `Snapshot`, `Steer`, `FollowUp`, `Abort`, or `Wait` concurrently.
Concurrent runs return `agent.ErrBusy`. Event callbacks run in order, outside the
agent's lock; they may inspect state or request cancellation. Do not call `Wait`
from the active run's callback. The agent becomes idle only after callbacks settle.

Steering is consumed one message per turn, after the current tool batch. Follow-up
messages are consumed only after the model would otherwise stop. Queues remain
available when the model-turn limit is reached. `Snapshot` copies built-in payloads;
custom message implementations must be immutable.

Supply a non-nil `codingagent.Options.Agent.Tools` slice to replace the default
tools, or use `codingagent.NewTools` and append custom tools before construction.
Set `SystemPrompt` to replace the default coding instructions. Working-directory
and ancestor `AGENTS.md` instructions are appended to either prompt. The agent is
instructed to inspect additional nested `AGENTS.md` files before editing there.

## Tool behavior

| Tool | Behavior |
| --- | --- |
| `read` | UTF-8 text with line numbers, one-based offset and line limit |
| `write` | Create or overwrite a file using atomic replacement |
| `edit` | Replace exactly one occurrence of `oldText`; ambiguous matches fail |
| `bash` | Execute a command in the working directory, streaming combined output |

File operations accept relative and absolute paths. Read/edit input is limited to
8 MiB and tool output to 64 KiB. Bash commands default to a 120-second timeout,
configurable per call up to 600 seconds. Bash must be available on `PATH`. On Unix,
cancellation and command completion clean up the command's process group. On other
platforms only the direct process is terminated.

Tools use the host process's filesystem, environment, and network permissions;
the working directory is not a sandbox. Hosted deployments need their own process
isolation and access controls.

Tool errors become model-visible results so the model can correct its request.
Tools run sequentially and only after the full model response. Truncated model
responses never execute tools. Parent cancellation stops the run; a command's own
timeout is a recoverable tool error. Side effects already performed cannot be
rolled back. The loop never automatically retries requests or replays old tools.

## Sessions and current boundaries

`Session.Save` and `Session.Load` operate on idle sessions. Saves use atomic JSON
replacement and file mode `0600`. History, reasoning, and pending queues are saved;
provider configuration and executable tool functions are not. Files contain the
full conversation and tool output. Restore requires the same
working directory, refreshes the initial project instructions, and executes nothing.
The built-in codec rejects custom message types; applications can provide their
own storage using `Agent.Snapshot` and `Agent.ReplaceMessages`.

The current provider supports text, reasoning, and tool calls. Image input is
rejected explicitly. There is no automatic context compaction, session branching,
plugin runtime, HTTP server, or Web UI yet. The library APIs provide the execution
boundary for those clients without coupling the agent to a transport.

## Validation

```sh
go test ./...
go test -race ./...
go vet ./...
```

Tests use fake model functions and local HTTP test servers. The coding tests use
temporary files and harmless local shell commands; they do not call real model
APIs, read API credentials, or consume paid tokens.
