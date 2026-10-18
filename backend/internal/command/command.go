// Package command implements nexus.v1.CommandService: the single entry point
// behind the command bar, Siri and the share sheet.
//
// Module "command" (hub, default enabled). Requires tasks and router.
// Optional services, resolved per call: "vault" (SEARCH/CAPTURE), "llm"
// (answers for SEARCH), "agents" (choices when the router has none).
//
// Execute behaviour by route kind:
//   - TASK (and UNSPECIFIED): creates a task for the routed agent.
//   - SEARCH: Vault.Search; with an LLM the hits are summarised into an
//     answer, otherwise the hits are returned as sources only. No vault →
//     Unavailable.
//   - CAPTURE: Vault.Capture; no vault → Unavailable.
//   - WORKFLOW: Unimplemented (no workflow engine yet).
//
// When the route's confidence is below `threshold` (or a TASK has no agent),
// Execute returns a question with choices instead of acting. Choices are
// agent ids, preceded by the literal "search" / "capture" when the router
// leaned towards those kinds. Clients answer by calling Execute again with a
// forced Route ({kind: TASK, agent_id: choice} or {kind: SEARCH|CAPTURE}).
// When the route says no agent fits (params.no_fit, set by the learning
// router) and the architect module is enabled, the last choice is
// "Create a new agent for this"; choosing it starts an architect draft
// (docs/ARCHITECT.md) instead of a task.
//
// Config (nexus.yaml):
//
//	command:
//	  threshold: 0.6
//	  answer_model: smart
//	  search_limit: 8
package command

import (
	"context"
	"errors"
	"fmt"
	"github.com/danielpina1/nexus/backend/internal/workflow"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/architect"
	"github.com/danielpina1/nexus/backend/internal/router"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() { kernel.RegisterModule(func() kernel.Module { return &Module{} }) }

// Config is the "command" config section.
type Config struct {
	Threshold   float64 `yaml:"threshold"`
	AnswerModel string  `yaml:"answer_model"`
	SearchLimit int     `yaml:"search_limit"`
	MaxChoices  int     `yaml:"max_choices"`
}

func (c *Config) defaults() {
	if c.Threshold <= 0 {
		c.Threshold = 0.6
	}
	if c.AnswerModel == "" {
		c.AnswerModel = "smart"
	}
	if c.SearchLimit <= 0 {
		c.SearchLimit = 8
	}
	if c.MaxChoices <= 0 {
		c.MaxChoices = 6
	}
}

const (
	kindSearch   = nexusv1.CommandKind_COMMAND_KIND_SEARCH
	kindCapture  = nexusv1.CommandKind_COMMAND_KIND_CAPTURE
	kindTask     = nexusv1.CommandKind_COMMAND_KIND_TASK
	kindWorkflow = nexusv1.CommandKind_COMMAND_KIND_WORKFLOW
)

// Optional capabilities of the router and task engine.
type (
	decisionLinker interface {
		LinkTask(ctx context.Context, decisionID, taskID string) error
	}
	correctionRecorder interface {
		RecordCorrection(ctx context.Context, c router.Correction) error
	}
	reassigner interface {
		ReassignTask(ctx context.Context, id, agentID string) (*nexusv1.Task, error)
	}
)

// Service implements nexusv1connect.CommandServiceHandler.
type Service struct {
	h      kernel.Host
	log    *slog.Logger
	cfg    Config
	tasks  kernel.TaskEngine
	router kernel.Router
}

var _ nexusv1connect.CommandServiceHandler = (*Service)(nil)

// New builds the service.
func New(h kernel.Host, tasks kernel.TaskEngine, rt kernel.Router, cfg Config) *Service {
	cfg.defaults()
	return &Service{h: h, log: h.Logger(), cfg: cfg, tasks: tasks, router: rt}
}

func lookup[T any](h kernel.Host, name string) (T, bool) {
	v, err := kernel.Resolve[T](h, name)
	return v, err == nil
}

// Plan returns the route without executing.
func (s *Service) Plan(ctx context.Context, req *connect.Request[nexusv1.PlanRequest]) (*connect.Response[nexusv1.PlanResponse], error) {
	route, err := s.router.Route(ctx, req.Msg.Input, req.Msg.Attachments, req.Msg.Context)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("route: %w", err))
	}
	return connect.NewResponse(&nexusv1.PlanResponse{Route: route}), nil
}

// Execute routes (unless a route is forced) and executes.
func (s *Service) Execute(ctx context.Context, req *connect.Request[nexusv1.ExecuteRequest]) (*connect.Response[nexusv1.ExecuteResponse], error) {
	m := req.Msg
	if strings.TrimSpace(m.Input) == "" && len(m.Attachments) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("input or attachments required"))
	}
	route, err := s.resolveRoute(ctx, m)
	if err != nil {
		return nil, err
	}
	resp := &nexusv1.ExecuteResponse{Route: route}
	if route.AgentId == architect.ChoiceCreateAgent { // "Create a new agent for this"
		if err := s.createAgent(ctx, m, resp); err != nil {
			return nil, err
		}
		return connect.NewResponse(resp), nil
	}

	if q, choices := s.needsChoice(ctx, route); q != "" {
		resp.Question, resp.Choices = q, choices
		return connect.NewResponse(resp), nil
	}

	switch route.Kind {
	case kindSearch:
		err = s.search(ctx, m, resp)
	case kindCapture:
		err = s.capture(ctx, m, resp)
	case kindWorkflow:
		err = s.workflow(ctx, m, route, resp)
	default:
		err = s.task(ctx, m, route, resp)
	}
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) resolveRoute(ctx context.Context, m *nexusv1.ExecuteRequest) (*nexusv1.Route, error) {
	if m.Route != nil {
		r := proto.Clone(m.Route).(*nexusv1.Route)
		if r.Kind == nexusv1.CommandKind_COMMAND_KIND_UNSPECIFIED {
			r.Kind = kindTask
		}
		if r.Confidence == 0 {
			r.Confidence = 1 // chosen by the user
		}
		if r.Reason == "" {
			r.Reason = "chosen by user"
		}
		return r, nil
	}
	// Workflow intents ("buy groceries", "morning brief") are matched
	// deterministically before the router so no model call is needed.
	if wf, ok := lookup[workflow.Workflows](s.h, workflow.SvcWorkflows); ok {
		if id, score := wf.MatchIntent(m.Input); id != "" && score >= workflowIntentScore {
			return &nexusv1.Route{Kind: kindWorkflow, WorkflowId: id, Confidence: float32(score), Reason: "workflow intent"}, nil
		}
	}
	r, err := s.router.Route(ctx, m.Input, m.Attachments, m.Context)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("route: %w", err))
	}
	if r.Kind == nexusv1.CommandKind_COMMAND_KIND_UNSPECIFIED {
		r.Kind = kindTask
	}
	return r, nil
}

// needsChoice returns a question and choices when the route is not confident
// enough to act on.
func (s *Service) needsChoice(ctx context.Context, r *nexusv1.Route) (string, []string) {
	confident := r.Confidence >= float32(s.cfg.Threshold)
	if confident && (r.Kind != kindTask || r.AgentId != "" || r.TeamId != "") {
		return "", nil
	}
	var choices []string
	switch r.Kind {
	case kindSearch:
		choices = append(choices, "search")
	case kindCapture:
		choices = append(choices, "capture")
	}
	agents := append([]string{r.AgentId}, r.Alternatives...)
	if r.AgentId == "" && len(r.Alternatives) == 0 {
		if cat, ok := lookup[kernel.AgentCatalog](s.h, kernel.SvcAgents); ok {
			if list, err := cat.ListAgents(ctx, false); err == nil {
				for _, a := range list {
					agents = append(agents, a.Id)
				}
			}
		}
	}
	seen := map[string]bool{"": true}
	for _, c := range choices {
		seen[c] = true
	}
	for _, a := range agents {
		if !seen[a] && len(choices) < s.cfg.MaxChoices {
			seen[a] = true
			choices = append(choices, a)
		}
	}
	// No agent fits well (learning router): offer to design one.
	if r.GetParams().GetFields()["no_fit"].GetBoolValue() {
		if _, ok := lookup[agentDrafter](s.h, architect.SvcArchitect); ok {
			if len(choices) >= s.cfg.MaxChoices {
				choices = choices[:s.cfg.MaxChoices-1]
			}
			choices = append(choices, architect.ChoiceCreateAgent)
		}
	}
	q := "Which agent should handle this?"
	if r.Kind == kindSearch || r.Kind == kindCapture {
		q = "Should I " + strings.ToLower(strings.TrimPrefix(r.Kind.String(), "COMMAND_KIND_")) + " this, or hand it to an agent?"
	}
	if len(choices) == 0 {
		q = "I couldn't tell what to do with this. Could you rephrase?"
	}
	return q, choices
}

func (s *Service) task(ctx context.Context, m *nexusv1.ExecuteRequest, r *nexusv1.Route, resp *nexusv1.ExecuteResponse) error {
	source := m.GetContext().GetSource()
	if source == "" {
		source = "command"
	}
	treq := &nexusv1.CreateTaskRequest{
		Prompt: m.Input, AgentId: r.AgentId, TeamId: r.TeamId, Attachments: m.Attachments,
		IdempotencyKey: m.IdempotencyKey, Source: source,
	}
	if p := r.GetParams(); p != nil {
		treq.Params = proto.Clone(p).(*structpb.Struct)
		delete(treq.Params.Fields, "decision_id")
		if len(treq.Params.Fields) == 0 {
			treq.Params = nil
		}
	}
	t, err := s.tasks.CreateTask(ctx, treq)
	if err != nil {
		return err
	}
	resp.Task = t
	if d := r.GetParams().GetFields()["decision_id"].GetStringValue(); d != "" {
		if l, ok := s.router.(decisionLinker); ok {
			if err := l.LinkTask(ctx, d, t.Id); err != nil {
				s.log.Warn("link router decision", "decision", d, "err", err)
			}
		}
	}
	return nil
}

func (s *Service) search(ctx context.Context, m *nexusv1.ExecuteRequest, resp *nexusv1.ExecuteResponse) error {
	v, ok := lookup[kernel.Vault](s.h, kernel.SvcVault)
	if !ok {
		return connect.NewError(connect.CodeUnavailable, errors.New("search needs the vault module"))
	}
	hits, err := v.Search(ctx, m.Input, nil, s.cfg.SearchLimit)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("vault search: %w", err))
	}
	for _, h := range hits {
		if h.GetNote() != nil {
			resp.Sources = append(resp.Sources, h.Note)
		}
	}
	if len(hits) == 0 {
		resp.Answer = "I couldn't find anything about that in your vault."
		return nil
	}
	llm, ok := lookup[kernel.LLM](s.h, kernel.SvcLLM)
	if !ok || !kernel.LLMAvailable(llm, s.cfg.AnswerModel) {
		return nil // hits only (no LLM, or its provider is degraded)
	}
	answer, err := s.answer(ctx, llm, m.Input, hits)
	if err != nil {
		s.log.Warn("search answer", "err", err)
		return nil
	}
	resp.Answer = answer
	return nil
}

const answerPrompt = `You answer questions about the user's own notes.
Use only the notes below. Be concise. Mention the titles of the notes you used.
If the notes do not contain the answer, say you could not find it.`

func (s *Service) answer(ctx context.Context, llm kernel.LLM, question string, hits []*nexusv1.SearchHit) (string, error) {
	var b strings.Builder
	for i, h := range hits {
		fmt.Fprintf(&b, "[%d] %s (%s)\n%s\n\n", i+1, h.GetNote().GetTitle(), h.GetNote().GetPath(), h.GetSnippet())
	}
	resp, err := llm.Chat(ctx, kernel.ChatRequest{
		Model: s.cfg.AnswerModel,
		Messages: []kernel.ChatMessage{
			{Role: "system", Content: answerPrompt},
			{Role: "user", Content: "Notes:\n" + b.String() + "Question: " + question},
		},
		MaxTokens: 600,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Message.Content), nil
}

func (s *Service) capture(ctx context.Context, m *nexusv1.ExecuteRequest, resp *nexusv1.ExecuteResponse) error {
	v, ok := lookup[kernel.Vault](s.h, kernel.SvcVault)
	if !ok {
		return connect.NewError(connect.CodeUnavailable, errors.New("capture needs the vault module"))
	}
	creq := &nexusv1.CaptureRequest{Text: m.Input, Attachments: m.Attachments, Source: m.GetContext().GetSource(), IdempotencyKey: m.IdempotencyKey}
	for _, a := range m.Attachments {
		if a.GetUrl() != "" && len(a.Data) == 0 {
			creq.Url = a.Url
			break
		}
	}
	ref, err := v.Capture(ctx, creq)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("capture: %w", err))
	}
	resp.Sources = []*nexusv1.NoteRef{ref}
	resp.Answer = "Saved to " + ref.GetPath()
	return nil
}

// CorrectRoute records that the user overrode the routing decision of a task
// and hands the task to the chosen agent (in place while it is still being
// routed, otherwise by cancelling it and creating a new task).
func (s *Service) CorrectRoute(ctx context.Context, req *connect.Request[nexusv1.CorrectRouteRequest]) (*connect.Response[nexusv1.CorrectRouteResponse], error) {
	m := req.Msg
	if m.TaskId == "" || m.AgentId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("task_id and agent_id required"))
	}
	old, err := s.tasks.GetTask(ctx, m.TaskId)
	if err != nil {
		return nil, err
	}
	if rec, ok := s.router.(correctionRecorder); ok {
		c := router.Correction{TaskID: old.Id, Input: old.Prompt, FromAgent: old.AgentId, ToAgent: m.AgentId, Comment: m.Comment}
		if err := rec.RecordCorrection(ctx, c); err != nil {
			s.log.Warn("record route correction", "task", old.Id, "err", err)
		}
	}
	if old.AgentId == m.AgentId {
		return connect.NewResponse(&nexusv1.CorrectRouteResponse{Task: old}), nil
	}
	var t *nexusv1.Task
	if ra, ok := s.tasks.(reassigner); ok {
		t, err = ra.ReassignTask(ctx, old.Id, m.AgentId)
	} else {
		if err = s.tasks.CancelTask(ctx, old.Id); err == nil {
			t, err = s.tasks.CreateTask(ctx, &nexusv1.CreateTaskRequest{Prompt: old.Prompt, AgentId: m.AgentId, Source: old.Source})
		}
	}
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.CorrectRouteResponse{Task: t}), nil
}

// ---------------------------------------------------------------- module

// Module is the command module.
type Module struct {
	kernel.BaseModule
	Service *Service
}

func (*Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "command",
		Description:    "Command bar / Siri / share-sheet entry point: plan, execute, correct routes",
		Side:           kernel.SideHub,
		Requires:       []string{"tasks", "router"},
		DefaultEnabled: true,
	}
}

func (m *Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return fmt.Errorf("command: config: %w", err)
	}
	tasks, err := kernel.Resolve[kernel.TaskEngine](h, kernel.SvcTasks)
	if err != nil {
		return err
	}
	rt, err := kernel.Resolve[kernel.Router](h, kernel.SvcRouter)
	if err != nil {
		return err
	}
	m.Service = New(h, tasks, rt, cfg)
	h.Handle(nexusv1connect.NewCommandServiceHandler(m.Service))
	return nil
}

// workflowIntentScore is the minimum MatchIntent score that starts a
// workflow without asking (0.8 = the input contains an intent phrase).
const workflowIntentScore = 0.8

// workflow starts a workflow run for a WORKFLOW route.
func (s *Service) workflow(ctx context.Context, m *nexusv1.ExecuteRequest, route *nexusv1.Route, resp *nexusv1.ExecuteResponse) error {
	wf, ok := lookup[workflow.Workflows](s.h, workflow.SvcWorkflows)
	if !ok {
		return connect.NewError(connect.CodeUnavailable, errors.New("workflow module not enabled"))
	}
	id := route.WorkflowId
	if id == "" {
		id, _ = wf.MatchIntent(m.Input)
	}
	if id == "" {
		return connect.NewError(connect.CodeNotFound, errors.New("no workflow matches"))
	}
	inputs := wf.IntentInputs(id, m.Input)
	if inputs == nil {
		inputs = map[string]any{}
	}
	for k, v := range route.GetParams().AsMap() {
		inputs[k] = v
	}
	runID, err := wf.StartWith(ctx, workflow.StartOptions{
		WorkflowID: id, Inputs: inputs, Source: "command", IdempotencyKey: m.IdempotencyKey,
	})
	if err != nil {
		return err
	}
	route.WorkflowId = id
	resp.WorkflowRunId = runID
	resp.Answer = fmt.Sprintf("Started workflow %s (run %s).", id, runID)
	return nil
}

// agentDrafter is the architect service (module "architect").
type agentDrafter interface {
	DraftFromCommand(ctx context.Context, input string, c *nexusv1.CommandContext) (*nexusv1.AgentDraft, error)
}

// createAgent answers the "Create a new agent for this" choice: the
// architect drafts an agent for the input (reviewed and approved later).
func (s *Service) createAgent(ctx context.Context, m *nexusv1.ExecuteRequest, resp *nexusv1.ExecuteResponse) error {
	ad, ok := lookup[agentDrafter](s.h, architect.SvcArchitect)
	if !ok {
		return connect.NewError(connect.CodeUnavailable, errors.New("the architect module is not enabled"))
	}
	d, err := ad.DraftFromCommand(ctx, m.Input, m.Context)
	if err != nil {
		return err
	}
	resp.Answer = "Designing a new agent for this (draft " + d.Id + "). You'll get a notice to review, test and approve it."
	if st, err := structpb.NewStruct(map[string]any{"architect_draft_id": d.Id}); err == nil {
		resp.Route.Params = st
	}
	return nil
}
