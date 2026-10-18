package tasks

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/kernel"
)

// TeamHandler exposes nexus.v1.TeamService: team definitions through the
// team catalog (vault teams/*.md), run views and messages through the engine.
type TeamHandler struct{ E *Engine }

var _ nexusv1connect.TeamServiceHandler = TeamHandler{}

func (h TeamHandler) catalog() (kernel.TeamCatalog, error) {
	cat, ok := h.E.teams()
	if !ok {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("no team catalog available (is the vault module enabled?)"))
	}
	return cat, nil
}

func (h TeamHandler) ListTeams(ctx context.Context, req *connect.Request[nexusv1.ListTeamsRequest]) (*connect.Response[nexusv1.ListTeamsResponse], error) {
	cat, err := h.catalog()
	if err != nil {
		return nil, err
	}
	list, err := cat.ListTeams(ctx, req.Msg.GetIncludeDisabled())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.ListTeamsResponse{Teams: list}), nil
}

func (h TeamHandler) GetTeam(ctx context.Context, req *connect.Request[nexusv1.GetTeamRequest]) (*connect.Response[nexusv1.GetTeamResponse], error) {
	cat, err := h.catalog()
	if err != nil {
		return nil, err
	}
	t, _, md, err := cat.GetTeam(ctx, req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&nexusv1.GetTeamResponse{Team: t, Markdown: md}), nil
}

func (h TeamHandler) UpsertTeam(ctx context.Context, req *connect.Request[nexusv1.UpsertTeamRequest]) (*connect.Response[nexusv1.UpsertTeamResponse], error) {
	cat, err := h.catalog()
	if err != nil {
		return nil, err
	}
	t, err := cat.UpsertTeam(ctx, req.Msg.GetMarkdown(), req.Msg.GetCommitMessage(), req.Msg.GetValidateOnly())
	if err != nil {
		var ce *connect.Error
		if errors.As(err, &ce) {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&nexusv1.UpsertTeamResponse{Team: t}), nil
}

func (h TeamHandler) GetTeamRun(ctx context.Context, req *connect.Request[nexusv1.GetTeamRunRequest]) (*connect.Response[nexusv1.GetTeamRunResponse], error) {
	if req.Msg.GetRootTaskId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("root_task_id required"))
	}
	run, err := h.E.TeamRun(ctx, req.Msg.RootTaskId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.GetTeamRunResponse{Run: run}), nil
}

func (h TeamHandler) SendTeamMessage(ctx context.Context, req *connect.Request[nexusv1.SendTeamMessageRequest]) (*connect.Response[nexusv1.SendTeamMessageResponse], error) {
	t, err := h.E.GetTask(ctx, req.Msg.GetRootTaskId())
	if err != nil {
		return nil, err
	}
	tree, err := h.E.loadTree(ctx, t.RootId)
	if err != nil {
		return nil, err
	}
	if tree == nil || tree.team == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("not a team run"))
	}
	m, err := h.E.teamMessage(ctx, tree, nil, req.Msg.GetTo(), req.Msg.GetText())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.SendTeamMessageResponse{Message: m}), nil
}
