package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/bitinit/pigo/internal/core/ai"
)

// ErrBusy means the requested operation requires an idle agentloop.
var ErrBusy = errors.New("agentloop: a run is already active")

// State is a snapshot. Built-in message payloads are independent copies; custom
// message implementations must be immutable. Err is the last run's error.
type State struct {
	Messages []ai.Message
	Partial  ai.Message
	Running  bool
	Steering []ai.Message
	FollowUp []ai.Message
	Err      error
}

// Agent owns one conversation. Methods are safe to call concurrently, but only
// one Prompt or Continue can run at a time. Callbacks execute outside the lock;
// they may inspect state, queue messages, or abort, but must not Wait on that run.
// Do not copy an Agent after construction.
type Agent struct {
	mu       sync.Mutex
	options  Options
	state    State
	baseline []ai.Message
	cancel   context.CancelFunc
	done     chan struct{}
}

// New creates a stateful agentloop. It owns its queues, so GetSteering/GetFollowUp
// must be nil; use Steer/FollowUp instead. Options and custom payloads are immutable.
func New(options Options, messages ...ai.Message) (*Agent, error) {
	if options.Stream == nil {
		return nil, errors.New("agentloop: Stream is required")
	}
	if options.GetSteering != nil || options.GetFollowUp != nil {
		return nil, errors.New("agentloop: use Steer and FollowUp with a stateful agent")
	}
	if options.MaxTurns < 0 {
		return nil, errors.New("agentloop: negative MaxTurns")
	}
	options.Tools = append([]Tool(nil), options.Tools...)
	for i := range options.Tools {
		options.Tools[i].Parameters = append([]byte(nil), options.Tools[i].Parameters...)
	}
	a := &Agent{options: options, state: State{Messages: ai.CloneMessages(messages)}}
	for _, message := range a.state.Messages {
		if _, ok := message.(ai.SystemMessage); !ok {
			break
		}
		a.baseline = append(a.baseline, ai.CloneMessage(message))
	}
	return a, nil
}

// Snapshot returns state after the most recently processed event.
func (a *Agent) Snapshot() State {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.state
	s.Messages = ai.CloneMessages(s.Messages)
	s.Partial = ai.CloneMessage(s.Partial)
	s.Steering = ai.CloneMessages(s.Steering)
	s.FollowUp = ai.CloneMessages(s.FollowUp)
	return s
}

// Prompt appends messages and runs synchronously. Use a caller-owned goroutine
// if the client needs to continue accepting input while the agent runs.
func (a *Agent) Prompt(ctx context.Context, messages ...ai.Message) error {
	if len(messages) == 0 {
		return errors.New("agentloop: prompt is empty")
	}
	return a.run(ctx, messages)
}

// Continue resumes tool results or pending input. It never retries old tools.
// A completed assistant response needs a queued message or a new Prompt.
func (a *Agent) Continue(ctx context.Context) error { return a.run(ctx, nil) }

func (a *Agent) run(ctx context.Context, input []ai.Message) (err error) {
	a.mu.Lock()
	if a.state.Running {
		a.mu.Unlock()
		return ErrBusy
	}
	if err := ctx.Err(); err != nil {
		a.mu.Unlock()
		return err
	}
	a.state.Messages = append(a.state.Messages, ai.CloneMessages(input)...)
	lastIsAssistant := false
	if len(a.state.Messages) > 0 {
		_, lastIsAssistant = a.state.Messages[len(a.state.Messages)-1].(ai.AssistantMessage)
	}
	if len(input) == 0 && lastIsAssistant && len(a.state.Steering) == 0 && len(a.state.FollowUp) > 0 {
		a.state.Messages = append(a.state.Messages, a.state.FollowUp[0])
		a.state.FollowUp = a.state.FollowUp[1:]
	}
	ctx, cancel := context.WithCancel(ctx)
	a.cancel, a.done = cancel, make(chan struct{})
	a.state.Running, a.state.Err, a.state.Partial = true, nil, nil
	history := ai.CloneMessages(a.state.Messages)
	options := a.options
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		a.state.Running, a.state.Partial, a.state.Err = false, nil, err
		a.cancel = nil
		close(a.done)
		a.mu.Unlock()
	}()
	options.GetSteering = func(context.Context) ([]ai.Message, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if len(a.state.Steering) == 0 {
			return nil, nil
		}
		message := a.state.Steering[0]
		a.state.Steering = a.state.Steering[1:]
		return []ai.Message{message}, nil
	}
	options.GetFollowUp = func(context.Context) ([]ai.Message, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if len(a.state.FollowUp) == 0 {
			return nil, nil
		}
		message := a.state.FollowUp[0]
		a.state.FollowUp = a.state.FollowUp[1:]
		return []ai.Message{message}, nil
	}
	options.OnEvent = func(event Event) {
		a.mu.Lock()
		switch event.Type {
		case EventMessageStart, EventMessageUpdate:
			a.state.Partial = ai.CloneMessage(event.Message)
		case EventMessageEnd:
			a.state.Partial = nil
			a.state.Messages = append(a.state.Messages, ai.CloneMessage(event.Message))
		case EventAgentEnd:
			a.state.Err = event.Err
		}
		a.mu.Unlock()
		if a.options.OnEvent != nil {
			a.options.OnEvent(event)
		}
	}
	history, err = Run(ctx, history, options)
	a.mu.Lock()
	a.state.Messages = ai.CloneMessages(history)
	a.mu.Unlock()
	return err
}

// Steer queues messages, one per turn, after the current tool batch finishes.
func (a *Agent) Steer(messages ...ai.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Steering = append(a.state.Steering, ai.CloneMessages(messages)...)
}

// FollowUp queues messages for after the model would otherwise stop.
func (a *Agent) FollowUp(messages ...ai.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.FollowUp = append(a.state.FollowUp, ai.CloneMessages(messages)...)
}

// Abort requests cooperative cancellation without waiting for callbacks to end.
func (a *Agent) Abort() {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Wait waits for the currently active run, including its callbacks. It returns
// the waiting context's error, not the run's error; inspect Snapshot().Err for that.
func (a *Agent) Wait(ctx context.Context) error {
	a.mu.Lock()
	running, done := a.state.Running, a.done
	a.mu.Unlock()
	if !running {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reset clears history and queues, keeping the initial system instructions.
func (a *Agent) Reset() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.Running {
		return ErrBusy
	}
	a.state = State{Messages: ai.CloneMessages(a.baseline)}
	return nil
}

// ReplaceMessages restores an idle conversation without executing it. Reset
// continues to use the original system instructions. Pending queues are cleared.
func (a *Agent) ReplaceMessages(messages []ai.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.Running {
		return ErrBusy
	}
	a.state = State{Messages: ai.CloneMessages(messages)}
	return nil
}
