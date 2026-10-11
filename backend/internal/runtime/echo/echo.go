// Package echo is a deterministic runtime for tests and demos (node module
// "runtime.echo", runtime name "echo").
//
// It streams the prompt back word by word as RunOutput events. If the
// prompt contains "?ask" it asks a question and echoes the answer; if it
// contains "?approve" it emits a pending Approval (task WAITING_APPROVAL) and
// waits for Resolve. It then finishes with TaskUpdated SUCCEEDED (or FAILED
// when the approval is denied, CANCELLED when cancelled).
package echo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Markers recognised in the prompt.
const (
	MarkAsk     = "?ask"
	MarkApprove = "?approve"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

// Config is the "runtime.echo" section.
type Config struct {
	// Delay between words (default 0).
	Delay time.Duration `yaml:"delay"`
}

// Module registers the echo runtime.
type Module struct{ kernel.BaseModule }

func (Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name: "runtime.echo", Description: "Deterministic echo runtime for tests and demos",
		Side: kernel.SideNode, DefaultEnabled: true,
	}
}

func (Module) Init(ctx context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	return h.Runtimes().Register(&Runtime{Delay: cfg.Delay})
}

// Runtime is the echo runtime.
type Runtime struct{ Delay time.Duration }

func (*Runtime) Name() string { return "echo" }
func (*Runtime) Capabilities() kernel.RuntimeCapabilities {
	return kernel.RuntimeCapabilities{Streaming: true, Steering: true, ApprovalHooks: true}
}
func (*Runtime) Available(context.Context) (string, bool) { return "1.0", true }

func (r *Runtime) Start(ctx context.Context, spec kernel.RunSpec) (kernel.Run, error) {
	if spec.Assignment == nil {
		return nil, errors.New("echo: missing assignment")
	}
	run := &run{
		a:       spec.Assignment,
		delay:   r.Delay,
		events:  make(chan *nexusv1.Event, 64),
		inputs:  make(chan *nexusv1.RunInput, 8),
		results: make(chan *nexusv1.ApprovalResolution, 8),
		done:    make(chan struct{}),
	}
	go run.loop(ctx)
	return run, nil
}

type run struct {
	a       *nexusv1.RunAssignment
	delay   time.Duration
	events  chan *nexusv1.Event
	inputs  chan *nexusv1.RunInput
	results chan *nexusv1.ApprovalResolution
	done    chan struct{} // closed on Cancel
	once    sync.Once
}

var errCancelled = errors.New("cancelled")

func (r *run) Events() <-chan *nexusv1.Event { return r.events }

func (r *run) Send(ctx context.Context, in *nexusv1.RunInput) error {
	select {
	case r.inputs <- in:
		return nil
	case <-r.done:
		return errCancelled
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *run) Resolve(ctx context.Context, res *nexusv1.ApprovalResolution) error {
	select {
	case r.results <- res:
		return nil
	case <-r.done:
		return errCancelled
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *run) Cancel(context.Context) error {
	r.once.Do(func() { close(r.done) })
	return nil
}

func (r *run) emit(ev *nexusv1.Event) {
	ev.TaskId, ev.RunId, ev.At = r.a.TaskId, r.a.RunId, timestamppb.Now()
	r.events <- ev
}

func (r *run) output(text string, final bool) {
	r.emit(&nexusv1.Event{Body: &nexusv1.Event_RunOutput{RunOutput: &nexusv1.RunOutput{Text: text, IsFinal: final}}})
}

func (r *run) state(s nexusv1.TaskState, summary string) {
	r.emit(&nexusv1.Event{Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: &nexusv1.Task{
		Id: r.a.TaskId, CurrentRunId: r.a.RunId, State: s, Summary: summary,
	}}}})
}

func (r *run) wait(ctx context.Context) error {
	if r.delay <= 0 {
		select {
		case <-r.done:
			return errCancelled
		case <-ctx.Done():
			return errCancelled
		default:
			return nil
		}
	}
	t := time.NewTimer(r.delay)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-r.done:
		return errCancelled
	case <-ctx.Done():
		return errCancelled
	}
}

func (r *run) loop(ctx context.Context) {
	defer close(r.events)
	prompt := r.a.Prompt
	words := strings.Fields(prompt)
	for i, w := range words {
		if r.wait(ctx) != nil {
			r.state(nexusv1.TaskState_TASK_STATE_CANCELLED, "cancelled")
			return
		}
		if i < len(words)-1 {
			w += " "
		}
		r.output(w, false)
	}

	if strings.Contains(prompt, MarkAsk) {
		qid := "q_" + r.a.RunId
		r.emit(&nexusv1.Event{Body: &nexusv1.Event_Question{Question: &nexusv1.Question{
			QuestionId: qid, Text: "Echo asks: what should I say?",
			Choices: []string{"yes", "no"}, AllowFreeText: true,
		}}})
		r.state(nexusv1.TaskState_TASK_STATE_WAITING_INPUT, "")
		var in *nexusv1.RunInput
		select {
		case in = <-r.inputs:
		case <-r.done:
			r.state(nexusv1.TaskState_TASK_STATE_CANCELLED, "cancelled")
			return
		case <-ctx.Done():
			r.state(nexusv1.TaskState_TASK_STATE_CANCELLED, "cancelled")
			return
		}
		answer := in.Choice
		if answer == "" {
			answer = in.Text
		}
		r.state(nexusv1.TaskState_TASK_STATE_RUNNING, "")
		r.output("\nanswer: "+answer, false)
	}

	if strings.Contains(prompt, MarkApprove) {
		approved, err := r.approval(ctx)
		if err != nil {
			r.state(nexusv1.TaskState_TASK_STATE_CANCELLED, "cancelled")
			return
		}
		if !approved {
			r.output("\napproval denied", true)
			r.state(nexusv1.TaskState_TASK_STATE_FAILED, "approval denied")
			return
		}
		r.state(nexusv1.TaskState_TASK_STATE_RUNNING, "")
		r.output("\napproved", false)
	}

	r.output("", true)
	r.state(nexusv1.TaskState_TASK_STATE_SUCCEEDED, fmt.Sprintf("echoed %d words", len(words)))
}

func (r *run) approval(ctx context.Context) (bool, error) {
	payload, _ := structpb.NewStruct(map[string]any{"action": "echo", "prompt": r.a.Prompt})
	hash := sha256.Sum256([]byte("echo\x00" + r.a.RunId + "\x00" + r.a.Prompt))
	challenge := make([]byte, 32)
	_, _ = rand.Read(challenge)
	apr := &nexusv1.Approval{
		Id: "apr_" + ulid.Make().String(), TaskId: r.a.TaskId, RunId: r.a.RunId,
		AgentId:     r.a.GetAgent().GetId(),
		ActionClass: nexusv1.ActionClass_ACTION_CLASS_COMPUTE,
		Level:       nexusv1.TrustLevel_TRUST_LEVEL_APPROVE,
		Title:       "Echo wants approval", Summary: "The echo runtime asks to continue.",
		Payload: payload, PayloadHash: hash[:], Challenge: challenge,
		State: nexusv1.ApprovalState_APPROVAL_STATE_PENDING, CreatedAt: timestamppb.Now(),
		ExpiresAt: timestamppb.New(time.Now().Add(time.Hour)),
	}
	r.emit(&nexusv1.Event{Body: &nexusv1.Event_Approval{Approval: apr}})
	r.state(nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL, "")
	for {
		select {
		case res := <-r.results:
			if id := res.GetApproval().GetId(); id != "" && id != apr.Id {
				continue // not ours
			}
			if d := res.GetDecision(); d != nil {
				return d.Approve, nil
			}
			return res.GetApproval().GetState() == nexusv1.ApprovalState_APPROVAL_STATE_APPROVED, nil
		case <-r.done:
			return false, errCancelled
		case <-ctx.Done():
			return false, errCancelled
		}
	}
}
