package tasks

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
)

// Handler exposes the engine as nexus.v1.TaskService.
type Handler struct{ E *Engine }

var _ nexusv1connect.TaskServiceHandler = Handler{}

func (h Handler) CreateTask(ctx context.Context, req *connect.Request[nexusv1.CreateTaskRequest]) (*connect.Response[nexusv1.CreateTaskResponse], error) {
	t, err := h.E.CreateTask(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.CreateTaskResponse{Task: t}), nil
}

func (h Handler) GetTask(ctx context.Context, req *connect.Request[nexusv1.GetTaskRequest]) (*connect.Response[nexusv1.GetTaskResponse], error) {
	t, err := h.E.GetTask(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	resp := &nexusv1.GetTaskResponse{Task: t}
	if req.Msg.IncludeChildren {
		if resp.Children, err = h.E.ChildTasks(ctx, t.Id); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(resp), nil
}

func (h Handler) ListTasks(ctx context.Context, req *connect.Request[nexusv1.ListTasksRequest]) (*connect.Response[nexusv1.ListTasksResponse], error) {
	ts, next, err := h.E.ListTasks(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.ListTasksResponse{Tasks: ts, NextPageToken: next}), nil
}

func (h Handler) SendMessage(ctx context.Context, req *connect.Request[nexusv1.SendMessageRequest]) (*connect.Response[nexusv1.SendMessageResponse], error) {
	if err := h.E.SendMessage(ctx, req.Msg); err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.SendMessageResponse{}), nil
}

func (h Handler) CancelTask(ctx context.Context, req *connect.Request[nexusv1.CancelTaskRequest]) (*connect.Response[nexusv1.CancelTaskResponse], error) {
	if err := h.E.CancelTask(ctx, req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.CancelTaskResponse{}), nil
}

func (h Handler) EscalateTask(ctx context.Context, req *connect.Request[nexusv1.EscalateTaskRequest]) (*connect.Response[nexusv1.EscalateTaskResponse], error) {
	t, err := h.E.EscalateTask(ctx, req.Msg.Id, req.Msg.Hint)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.EscalateTaskResponse{Task: t}), nil
}

// StopAll cancels every active task. owner_lock_statement is not interpreted
// here; distributing the owner-signed lock to nodes is the security/nodelink
// modules' job.
func (h Handler) StopAll(ctx context.Context, req *connect.Request[nexusv1.StopAllRequest]) (*connect.Response[nexusv1.StopAllResponse], error) {
	n, err := h.E.StopAll(ctx, req.Msg.PauseTriggers)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.StopAllResponse{Cancelled: int32(n)}), nil
}

func (h Handler) FollowTask(ctx context.Context, req *connect.Request[nexusv1.FollowTaskRequest]) (*connect.Response[nexusv1.FollowTaskResponse], error) {
	if req.Msg.TaskId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("task_id required"))
	}
	if err := h.E.FollowTask(ctx, req.Msg.TaskId, req.Msg.DeviceIds); err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.FollowTaskResponse{}), nil
}
