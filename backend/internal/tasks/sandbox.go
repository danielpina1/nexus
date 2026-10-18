package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Sandbox test runs (docs/ARCHITECT.md). CreateSandboxTask runs an
// UNPUBLISHED agent definition (an architect draft) as a real task under a
// strict policy that the hub enforces at every assignment:
//
//   - tools: only hub tools of class read or compute; node tools, tools of
//     any other class, human-in-the-loop tools (ask_user), sub-task tools
//     (spawn_agent, schedule, team.*) and coding-only tools are removed;
//   - policy: every class from write_internal up is set to biometric, so a
//     node's approval gate asks before any such action — and the hub DENIES
//     every approval of a sandboxed task at once (never offered to the
//     owner), recording it as a blocked action;
//   - no sub-tasks (the tree allows one task), no review loop, no memory
//     writes, never the anthrex runtime;
//   - a small budget and wall-clock cap (tasks.sandbox), enforced as a
//     tree limit with a deadline timer;
//   - the task is labelled ("[sandbox] …", source "sandbox", params
//     sandbox: true) and hidden from ListTasks (reachable by id).
//
// The architect reads the outcome with SandboxReport.

// SourceSandbox is the Task.source of sandboxed tasks.
const SourceSandbox = "sandbox"

// SandboxAgentPrefix prefixes the ephemeral agent id of sandboxed tasks.
const SandboxAgentPrefix = ephPrefix + "sandbox-"

// SandboxConfig is the "tasks.sandbox" config section.
type SandboxConfig struct {
	Timeout    time.Duration `yaml:"timeout"`      // default 5m
	MaxCostEUR float64       `yaml:"max_cost_eur"` // default 0.20
	MaxRuns    int           `yaml:"max_runs"`     // default 2 (one run + one output repair)
}

func (c *SandboxConfig) defaults() {
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Minute
	}
	if c.MaxCostEUR <= 0 {
		c.MaxCostEUR = 0.20
	}
	if c.MaxRuns <= 0 {
		c.MaxRuns = 2
	}
}

// sandboxAction is one action a sandboxed run attempted and the hub refused.
type sandboxAction struct {
	Class  string `json:"class"`
	Title  string `json:"title,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Reason string `json:"reason"`
	At     string `json:"at,omitempty"`
}

func (a sandboxAction) String() string {
	s := a.Class
	if a.Tool != "" {
		s += " " + a.Tool
	}
	if a.Title != "" {
		s += ": " + a.Title
	}
	return s + " (" + a.Reason + ")"
}

// sandboxNeverTools are refused in a sandbox whatever their class: they
// would create work outside the sandbox or wait for a human.
var sandboxNeverTools = map[string]bool{"ask_user": true, "spawn_agent": true, "schedule": true, "request_approval": true}

func sandboxSafeClass(c nexusv1.ActionClass) bool {
	return c == nexusv1.ActionClass_ACTION_CLASS_READ || c == nexusv1.ActionClass_ACTION_CLASS_COMPUTE
}

func className(c nexusv1.ActionClass) string {
	return strings.ToLower(strings.TrimPrefix(c.String(), "ACTION_CLASS_"))
}

// strictClasses are forced to biometric (gate asks → hub denies).
var strictClasses = []string{"write_internal", "write_external", "code_push", "spend", "destructive", "device_control"}

// sandboxAgent applies the strict sandbox policy to an agent copy and
// returns the tools it removed (with why).
func (e *Engine) sandboxAgent(a *nexusv1.Agent) (*nexusv1.Agent, []sandboxAction) {
	c := proto.Clone(a).(*nexusv1.Agent)
	specs := map[string]kernel.ToolSpec{}
	for _, s := range e.h.Tools().List() {
		specs[s.Name] = s
	}
	var removed []sandboxAction
	var tools []string
	for _, t := range c.Tools {
		spec, hub := specs[t]
		switch {
		case sandboxNeverTools[t] || strings.HasPrefix(t, "team.") || isCodeOnlyTool(t):
			removed = append(removed, sandboxAction{Class: "tool", Tool: t, Reason: "not available in the sandbox"})
		case !hub:
			removed = append(removed, sandboxAction{Class: "tool", Tool: t, Reason: "node tools do not run in the sandbox"})
		case !sandboxSafeClass(spec.ActionClass):
			removed = append(removed, sandboxAction{Class: className(spec.ActionClass), Tool: t, Reason: "removed: only read/compute tools run in the sandbox"})
		default:
			tools = addTools(tools, t)
		}
	}
	c.Tools = tools
	if c.Policy == nil {
		c.Policy = map[string]nexusv1.TrustLevel{}
	}
	for _, k := range strictClasses {
		c.Policy[k] = nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC
	}
	if c.Runtime == "anthrex" {
		c.Runtime = kernel.RuntimeAuto
	}
	c.Review, c.Skills = nil, nil
	if c.Memory != nil {
		c.Memory.Writes = ""
	}
	lim := c.GetLimits()
	if lim == nil {
		lim = &nexusv1.AgentLimits{}
	}
	capS := int64(e.cfg.Sandbox.Timeout / time.Second)
	if lim.MaxRuntimeSeconds <= 0 || lim.MaxRuntimeSeconds > capS {
		lim.MaxRuntimeSeconds = capS
	}
	capC := e.sandboxBudget()
	if lim.MaxCost == nil || lim.MaxCost.MinorUnits <= 0 || lim.MaxCost.MinorUnits > capC.MinorUnits {
		lim.MaxCost = capC
	}
	lim.MaxSubtasks, lim.MaxDepth, lim.MaxParallel, lim.MaxTasks, lim.MaxRuns = 0, 0, 0, 0, 0
	lim.MaxTreeCost = nil
	c.Limits = lim
	c.Enabled = true
	return c, removed
}

func (e *Engine) sandboxBudget() *nexusv1.Money {
	return &nexusv1.Money{Currency: "EUR", MinorUnits: max(int64(e.cfg.Sandbox.MaxCostEUR*100+0.5), 1)}
}

// CreateSandboxTask runs an unpublished agent definition as a sandboxed task
// (implements architect.SandboxTaskCreator). The returned task is assigned
// (or failed); callers poll it until it is terminal and read the refused
// actions with SandboxReport.
func (e *Engine) CreateSandboxTask(ctx context.Context, agent *nexusv1.Agent, instructions string, req *nexusv1.CreateTaskRequest) (*nexusv1.Task, error) {
	if agent == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("agent required"))
	}
	if req == nil || (strings.TrimSpace(req.Prompt) == "" && len(req.Attachments) == 0) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("prompt or attachments required"))
	}
	req = proto.Clone(req).(*nexusv1.CreateTaskRequest)
	params := map[string]any{}
	if req.Params != nil {
		params = req.Params.AsMap()
	}
	delete(params, ParamRuntimeHint)
	params["sandbox"] = true
	if req.Source != "" && req.Source != SourceSandbox {
		params["sandbox_source"] = req.Source
	}
	ps, err := structpb.NewStruct(jsonSafeMap(params))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	req.Params, req.Source, req.TeamId, req.AgentId, req.DryRun = ps, SourceSandbox, "", "", false

	a, removed := e.sandboxAgent(agent)
	a.Id = SandboxAgentPrefix + nonEmpty(strings.TrimPrefix(agent.Id, "sandbox-"), "draft")
	aj, err := protojson.Marshal(a)
	if err != nil {
		return nil, err
	}
	instr := strings.TrimSpace(instructions) + "\n\n## Sandbox test run\nThis is a sandboxed test run of an unpublished agent. " +
		"Read-only and compute tools work. Anything that writes, sends, buys, deletes, pushes code or controls a device is refused by the hub: " +
		"do not retry a refused action — say what you would have done and continue."
	x := &taskExt{AgentJSON: aj, Instructions: strings.TrimSpace(instr), Sandbox: true, SandboxBlocked: removed}
	r, existing, err := e.insertNew(ctx, req, nil, createOpts{agentID: a.Id, ext: x, sandbox: true})
	if err != nil || existing {
		if r == nil {
			return nil, err
		}
		return r.task, err
	}
	// One task, a couple of runs, a small budget and a deadline.
	tree := &treeRec{rootID: r.task.Id, limits: &nexusv1.TeamLimits{MaxTasks: 1, MaxParallel: 1, MaxRuns: int32(e.cfg.Sandbox.MaxRuns),
		MaxCost: e.sandboxBudget(), TimeoutSeconds: int64(e.cfg.Sandbox.Timeout / time.Second)}, deadline: e.now().Add(e.cfg.Sandbox.Timeout)}
	if err := e.insertTree(ctx, tree); err != nil {
		return nil, err
	}
	e.startTimer(r.task.Id, e.cfg.Sandbox.Timeout)
	return e.assign(ctx, r.task.Id, a.Id, assignOpts{})
}

// SandboxReport returns the tool calls a sandboxed task made and the
// actions the hub refused (tools removed up front, then every gated action
// it attempted), for architect test results.
func (e *Engine) SandboxReport(ctx context.Context, taskID string) (calls, blocked []string, err error) {
	x, err := e.loadExt(ctx, taskID)
	if err != nil {
		return nil, nil, err
	}
	if !x.Sandbox {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("task %s is not a sandbox task", taskID))
	}
	for _, a := range x.SandboxBlocked {
		blocked = append(blocked, a.String())
	}
	return append([]string(nil), x.SandboxCalls...), blocked, nil
}

// isSandboxed reports whether a task is a sandbox test run.
func (e *Engine) isSandboxed(ctx context.Context, taskID string) (*taskExt, bool) {
	x, err := e.loadExt(ctx, taskID)
	if err != nil || !x.Sandbox {
		return nil, false
	}
	return x, true
}

// prepareSandbox re-applies the strict policy at every assignment (skills,
// repairs and retries cannot widen it) and marks the run context.
func (e *Engine) prepareSandbox(p *prepared, set func(string, any)) {
	p.agent, _ = e.sandboxAgent(p.agent)
	set("sandbox", true)
	// Non-coding: the CLI runtimes keep their file/shell tools disabled.
	set("claude", map[string]any{"coding": false})
	set("codex", map[string]any{"coding": false})
}

// sandboxDeny refuses an approval requested by a sandboxed run: it is never
// registered with the broker (nobody is asked); the node gets a denial at
// once and the action is recorded. If the denial cannot be delivered the
// run is cancelled (fail closed).
func (e *Engine) sandboxDeny(ctx context.Context, nodeID string, run *runRec, a *nexusv1.Approval) error {
	a = proto.Clone(a).(*nexusv1.Approval)
	a.TaskId, a.RunId, a.AgentId, a.NodeId = run.taskID, run.id, run.agentID, nodeID
	a.State = nexusv1.ApprovalState_APPROVAL_STATE_DENIED
	act := sandboxAction{Class: className(a.ActionClass), Title: nonEmpty(a.Title, a.Summary), Reason: "denied: sandbox test run",
		At: e.now().UTC().Format(time.RFC3339)}
	if tool := a.GetPayload().GetFields()["tool"].GetStringValue(); tool != "" {
		act.Tool = tool
	}
	if _, err := e.updateExt(ctx, run.taskID, func(x *taskExt) { x.SandboxBlocked = append(x.SandboxBlocked, act) }); err != nil {
		return err
	}
	e.log.Info("sandbox: action denied", "task", run.taskID, "class", act.Class, "title", act.Title)
	e.publish(ctx, &nexusv1.Event{TaskId: run.taskID, RunId: run.id, Actor: actorHub, Body: &nexusv1.Event_RunOutput{RunOutput: &nexusv1.RunOutput{
		Text: "[sandbox] blocked " + act.String() + "\n"}}})
	if _, err := e.mutate(ctx, run.taskID, "node:"+nodeID, func(r *taskRec) error {
		if r.task.CurrentRunId != run.id || IsTerminal(r.task.State) {
			return nil
		}
		return e.accept(ctx, r, run.id)
	}); err != nil {
		return err
	}
	disp, ok := e.dispatcher()
	if ok {
		err := disp.Resolve(ctx, nodeID, &nexusv1.ApprovalResolution{RunId: run.id, Approval: a})
		if err == nil {
			return nil
		}
		e.log.Warn("sandbox: deliver denial", "task", run.taskID, "err", err)
	}
	_, err := e.cancel(ctx, run.taskID, "sandbox: a refused action could not be denied on the node")
	return err
}

// recordSandboxCall notes a tool call of a sandboxed run (refused actions
// are recorded when the node's gate asks, see sandboxDeny).
func (e *Engine) recordSandboxCall(ctx context.Context, taskID string, tc *nexusv1.ToolCall) {
	if _, ok := e.isSandboxed(ctx, taskID); !ok || tc.GetTool() == "" {
		return
	}
	if _, err := e.updateExt(ctx, taskID, func(x *taskExt) { x.SandboxCalls = append(x.SandboxCalls, tc.Tool) }); err != nil {
		e.log.Warn("sandbox: record tool call", "task", taskID, "err", err)
	}
}
