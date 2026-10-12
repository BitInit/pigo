// Package tools provides the basic coding tools.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bitinit/pigo/internal/core/agentloop"
	"github.com/bitinit/pigo/internal/core/ai"
	"github.com/bitinit/pigo/internal/storage"
)

const maxFileBytes = 8 << 20
const maxOutputBytes = 64 << 10

// New creates pi-style read, write, edit, and bash tools. Relative paths and
// commands use directory. Absolute paths are allowed. This is not a sandbox:
// tools run with the host process's filesystem, network, and environment access.
func New(directory string) ([]agentloop.Tool, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("codingagent: %s is not a directory", directory)
	}
	t := fileTools{directory: directory}
	return []agentloop.Tool{
		{Tool: ai.Tool{Name: "read", Description: "Read a UTF-8 text file with line numbers. Offset is one-based; default limit is 2000 lines. Output is bounded to 64 KiB; use offset/limit for more.", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1},"offset":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1}},"required":["path"],"additionalProperties":false}`)}, Execute: t.read},
		{Tool: ai.Tool{Name: "write", Description: "Create or overwrite a text file. Creates parent directories. Read existing files before replacing them.", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`)}, Execute: t.write},
		{Tool: ai.Tool{Name: "edit", Description: "Replace exactly one occurrence of oldText with newText. Fails if oldText is absent or ambiguous; read the file first.", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1},"oldText":{"type":"string","minLength":1},"newText":{"type":"string"}},"required":["path","oldText","newText"],"additionalProperties":false}`)}, Execute: t.edit},
		{Tool: ai.Tool{Name: "bash", Description: "Run a bash command in the working directory. Use for listing, searching, and tests. Default timeout is 120 seconds; maximum 600 seconds. Output is limited to 64 KiB.", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","minLength":1},"timeoutSeconds":{"type":"integer","minimum":1,"maximum":600}},"required":["command"],"additionalProperties":false}`)}, Execute: t.bash},
	}, nil
}

type fileTools struct{ directory string }

func (t fileTools) path(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path is required")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(t.directory, path)
	}
	return filepath.Clean(path), nil
}

// T is constrained only by the concrete argument structs at each call site.
func decode[T any](raw json.RawMessage) (T, error) {
	var args T
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return args, errors.New("arguments must be one JSON object")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	err := d.Decode(&args)
	return args, err
}

func textResult(text string) ai.ToolResultMessage {
	return ai.ToolResultMessage{Content: []ai.ContentBlock{ai.TextContent{Text: text}}}
}

func (t fileTools) read(ctx context.Context, call ai.ToolCall, _ func(ai.ToolResultMessage)) (ai.ToolResultMessage, error) {
	args, err := decode[struct {
		Path   string
		Offset *int
		Limit  *int
	}](call.Arguments)
	if err != nil {
		return ai.ToolResultMessage{}, err
	}
	path, err := t.path(args.Path)
	if err != nil {
		return ai.ToolResultMessage{}, err
	}
	offset, limit := 1, 2000
	if args.Offset != nil {
		offset = *args.Offset
	}
	if args.Limit != nil {
		limit = *args.Limit
	}
	if offset < 1 || limit < 1 {
		return ai.ToolResultMessage{}, errors.New("offset and limit must be positive")
	}
	if err := ctx.Err(); err != nil {
		return ai.ToolResultMessage{}, err
	}
	data, err := storage.ReadText(path, maxFileBytes)
	if err != nil {
		return ai.ToolResultMessage{}, err
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if offset > len(lines) {
		return ai.ToolResultMessage{}, fmt.Errorf("offset exceeds %d lines", len(lines))
	}
	var out strings.Builder
	end := offset - 1
	for end < len(lines) && end-(offset-1) < limit {
		line := fmt.Sprintf("%d: %s\n", end+1, lines[end])
		if out.Len()+len(line) > maxOutputBytes {
			remaining := maxOutputBytes - out.Len()
			out.WriteString(strings.ToValidUTF8(line[:remaining], ""))
			out.WriteString("\n[output truncated within this line; use bash to inspect a narrower portion]\n")
			return textResult(out.String()), ctx.Err()
		}
		out.WriteString(line)
		end++
	}
	if end < len(lines) {
		fmt.Fprintf(&out, "[more lines: continue with offset=%d]\n", end+1)
	}
	return textResult(out.String()), ctx.Err()
}

func (t fileTools) write(ctx context.Context, call ai.ToolCall, _ func(ai.ToolResultMessage)) (ai.ToolResultMessage, error) {
	args, err := decode[struct {
		Path    string
		Content *string
	}](call.Arguments)
	if err != nil {
		return ai.ToolResultMessage{}, err
	}
	if args.Content == nil {
		return ai.ToolResultMessage{}, errors.New("content is required")
	}
	path, err := t.path(args.Path)
	if err != nil {
		return ai.ToolResultMessage{}, err
	}
	if len(*args.Content) > maxFileBytes {
		return ai.ToolResultMessage{}, errors.New("content exceeds 8 MiB")
	}
	if err := ctx.Err(); err != nil {
		return ai.ToolResultMessage{}, err
	}
	if err := writeFile(ctx, path, []byte(*args.Content)); err != nil {
		return ai.ToolResultMessage{}, err
	}
	return textResult(fmt.Sprintf("Wrote %d bytes to %s", len(*args.Content), path)), nil
}

func (t fileTools) edit(ctx context.Context, call ai.ToolCall, _ func(ai.ToolResultMessage)) (ai.ToolResultMessage, error) {
	args, err := decode[struct {
		Path    string
		OldText string
		NewText *string
	}](call.Arguments)
	if err != nil {
		return ai.ToolResultMessage{}, err
	}
	if args.OldText == "" || args.NewText == nil {
		return ai.ToolResultMessage{}, errors.New("non-empty oldText and newText are required")
	}
	path, err := t.path(args.Path)
	if err != nil {
		return ai.ToolResultMessage{}, err
	}
	if err := ctx.Err(); err != nil {
		return ai.ToolResultMessage{}, err
	}
	data, err := storage.ReadText(path, maxFileBytes)
	if err != nil {
		return ai.ToolResultMessage{}, err
	}
	if count := strings.Count(string(data), args.OldText); count != 1 {
		return ai.ToolResultMessage{}, fmt.Errorf("oldText must match exactly once, found %d matches", count)
	}
	next := strings.Replace(string(data), args.OldText, *args.NewText, 1)
	if len(next) > maxFileBytes {
		return ai.ToolResultMessage{}, errors.New("edited file exceeds 8 MiB")
	}
	if err := writeFile(ctx, path, []byte(next)); err != nil {
		return ai.ToolResultMessage{}, err
	}
	return textResult("Updated " + path), nil
}

func writeFile(ctx context.Context, path string, data []byte) error {
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("destination is not a regular file")
		}
		mode = info.Mode().Perm()
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return storage.WriteAtomic(ctx, path, data, mode)
}

type commandOutput struct {
	data      []byte
	truncated bool
	update    func(ai.ToolResultMessage)
}

func (o *commandOutput) Write(data []byte) (int, error) {
	n := len(data)
	remaining := maxOutputBytes - len(o.data)
	if len(data) > remaining {
		data = data[:remaining]
		o.truncated = true
	}
	o.data = append(o.data, data...)
	if len(data) > 0 && o.update != nil {
		o.update(textResult(strings.ToValidUTF8(string(o.data), "")))
	}
	return n, nil
}

func (t fileTools) bash(ctx context.Context, call ai.ToolCall, update func(ai.ToolResultMessage)) (ai.ToolResultMessage, error) {
	args, err := decode[struct {
		Command        string
		TimeoutSeconds *int
	}](call.Arguments)
	if err != nil {
		return ai.ToolResultMessage{}, err
	}
	if strings.TrimSpace(args.Command) == "" {
		return ai.ToolResultMessage{}, errors.New("command is required")
	}
	seconds := 120
	if args.TimeoutSeconds != nil {
		seconds = *args.TimeoutSeconds
	}
	if seconds < 1 || seconds > 600 {
		return ai.ToolResultMessage{}, errors.New("timeoutSeconds must be between 1 and 600")
	}
	if err := ctx.Err(); err != nil {
		return ai.ToolResultMessage{}, err
	}
	commandCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "bash", "-c", args.Command)
	cmd.Dir, cmd.WaitDelay = t.directory, 2*time.Second
	configureProcess(cmd)
	output := &commandOutput{update: update}
	// os/exec serializes writes when Stdout and Stderr are the same writer.
	cmd.Stdout, cmd.Stderr = output, output
	err = cmd.Run()
	// A shell may exit while background descendants still own its pipes. Always
	// clean up the command's process group, including after WaitDelay expires.
	if cmd.Process != nil {
		_ = cmd.Cancel()
	}
	text := strings.ToValidUTF8(string(output.data), "")
	if output.truncated {
		text += "\n[output truncated at 64 KiB]"
	}
	result := textResult(text)
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	result.Details, _ = json.Marshal(struct {
		ExitCode  int  `json:"exitCode"`
		Truncated bool `json:"truncated"`
	}{exitCode, output.truncated})
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if commandCtx.Err() != nil {
		return result, fmt.Errorf("command timed out after %d seconds", seconds)
	}
	if err != nil {
		return result, fmt.Errorf("command failed (exit %d): %w", exitCode, err)
	}
	return result, nil
}
