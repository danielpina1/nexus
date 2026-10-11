package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// ErrNotImplemented is returned by stub tools.
var ErrNotImplemented = errors.New("not implemented yet")

// hubTools returns the hub-side tools owned by the task engine.
func (e *Engine) hubTools() []kernel.Tool {
	return []kernel.Tool{
		kernel.ToolFunc{S: kernel.ToolSpec{
			Name:        "ask_user",
			Description: "Ask the user a question (optionally with choices). Returns a question_id immediately; the answer arrives later as user input for this run.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"question":{"type":"string"},"choices":{"type":"array","items":{"type":"string"}},"allow_free_text":{"type":"boolean"}},"required":["question"]}`),
			ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ,
		}, F: e.toolAskUser},
		kernel.ToolFunc{S: kernel.ToolSpec{
			Name:        "notify",
			Description: "Send a notice to the user's Inbox (no response needed).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"body":{"type":"string"},"kind":{"type":"string","enum":["result","alert","suggestion","brief"]}},"required":["title"]}`),
			ActionClass: nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL,
		}, F: e.toolNotify},
		kernel.ToolFunc{S: kernel.ToolSpec{
			Name:        "spawn_agent",
			Description: "Create a sub-task for another agent (by agent_id, or leave it empty to let the router choose). Returns the child task id.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string"},"agent_id":{"type":"string"},"params":{"type":"object"}},"required":["prompt"]}`),
			ActionClass: nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL,
		}, F: e.toolSpawn},
		kernel.ToolFunc{S: kernel.ToolSpec{
			Name:        "schedule",
			Description: "Create a reminder or follow-up task in the future (not implemented yet).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"at":{"type":"string","format":"date-time"},"prompt":{"type":"string"},"agent_id":{"type":"string"}},"required":["at","prompt"]}`),
			ActionClass: nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL,
		}, F: func(context.Context, kernel.ToolCall) (kernel.ToolResult, error) {
			return kernel.ToolResult{}, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("schedule: %w", ErrNotImplemented))
		}},
	}
}

// callTask resolves the task a tool call belongs to.
func (e *Engine) callTask(ctx context.Context, c kernel.ToolCall) (string, error) {
	if c.TaskID != "" {
		return c.TaskID, nil
	}
	if c.RunID != "" {
		run, err := e.loadRun(ctx, c.RunID)
		if err != nil {
			return "", err
		}
		return run.taskID, nil
	}
	return "", connect.NewError(connect.CodeInvalidArgument, errors.New("tool call without task or run"))
}

func (e *Engine) toolAskUser(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
	var in struct {
		Question      string   `json:"question"`
		Choices       []string `json:"choices"`
		AllowFreeText *bool    `json:"allow_free_text"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return kernel.ToolResult{}, err
	}
	if strings.TrimSpace(in.Question) == "" {
		return kernel.ToolResult{}, errors.New("ask_user: question required")
	}
	taskID, err := e.callTask(ctx, c)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	free := len(in.Choices) == 0
	if in.AllowFreeText != nil {
		free = *in.AllowFreeText
	}
	qid, err := e.AskUser(ctx, taskID, c.RunID, &nexusv1.Question{Text: in.Question, Choices: in.Choices, AllowFreeText: free})
	if err != nil {
		return kernel.ToolResult{}, err
	}
	return kernel.ToolOutput(map[string]any{"question_id": qid, "status": "pending"})
}

var noticeKinds = map[string]nexusv1.NoticeKind{
	"result":     nexusv1.NoticeKind_NOTICE_KIND_RESULT,
	"alert":      nexusv1.NoticeKind_NOTICE_KIND_ALERT,
	"suggestion": nexusv1.NoticeKind_NOTICE_KIND_SUGGESTION,
	"brief":      nexusv1.NoticeKind_NOTICE_KIND_BRIEF,
}

func (e *Engine) toolNotify(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Kind  string `json:"kind"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return kernel.ToolResult{}, err
	}
	if strings.TrimSpace(in.Title) == "" {
		return kernel.ToolResult{}, errors.New("notify: title required")
	}
	kind, ok := noticeKinds[strings.ToLower(in.Kind)]
	if !ok {
		kind = nexusv1.NoticeKind_NOTICE_KIND_RESULT
	}
	taskID, _ := e.callTask(ctx, c) // notices may be task-less
	actor := actorHub
	if c.AgentID != "" {
		actor = "agent:" + c.AgentID
	}
	id := newID("ntc_")
	e.publish(ctx, &nexusv1.Event{TaskId: taskID, RunId: c.RunID, Actor: actor, Body: &nexusv1.Event_Notice{Notice: &nexusv1.Notice{
		Id: id, Kind: kind, Title: in.Title, Body: in.Body,
	}}})
	return kernel.ToolOutput(map[string]any{"notice_id": id})
}

func (e *Engine) toolSpawn(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
	var in struct {
		Prompt  string         `json:"prompt"`
		AgentID string         `json:"agent_id"`
		Params  map[string]any `json:"params"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return kernel.ToolResult{}, err
	}
	parentID, err := e.callTask(ctx, c)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	parent, err := e.loadTask(ctx, parentID)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	if parent.depth+1 > e.cfg.MaxDepth {
		return kernel.ToolResult{}, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("spawn_agent: max sub-task depth %d reached", e.cfg.MaxDepth))
	}
	if limit := e.maxSubtasks(ctx, parent.task.AgentId); limit > 0 {
		kids, err := e.ChildTasks(ctx, parentID)
		if err != nil {
			return kernel.ToolResult{}, err
		}
		if len(kids) >= limit {
			return kernel.ToolResult{}, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("spawn_agent: agent limit of %d sub-tasks reached", limit))
		}
	}
	req := &nexusv1.CreateTaskRequest{Prompt: in.Prompt, AgentId: in.AgentID, Source: "agent"}
	if len(in.Params) > 0 {
		if req.Params, err = structpb.NewStruct(in.Params); err != nil {
			return kernel.ToolResult{}, err
		}
	}
	child, err := e.CreateChildTask(ctx, parentID, req)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	return kernel.ToolOutput(map[string]any{
		"task_id": child.Id, "agent_id": child.AgentId, "state": stateName(child.State), "summary": child.Summary,
	})
}

func (e *Engine) maxSubtasks(ctx context.Context, agentID string) int {
	cat, ok := e.agents()
	if !ok || agentID == "" {
		return 0
	}
	a, _, _, err := cat.GetAgent(ctx, agentID)
	if err != nil {
		return 0
	}
	return int(a.GetLimits().GetMaxSubtasks())
}
