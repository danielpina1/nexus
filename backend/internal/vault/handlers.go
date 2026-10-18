package vault

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
)

// VaultHandler implements nexusv1connect.VaultServiceHandler.
type VaultHandler struct {
	V *Vault
	// BackupConfig returns the configured backup options (nil = backups disabled).
	BackupConfig func() *BackupOptions
}

// AgentHandler implements nexusv1connect.AgentServiceHandler.
type AgentHandler struct {
	C *Catalog
	S *SkillCatalog
}

var (
	_ nexusv1connect.VaultServiceHandler = VaultHandler{}
	_ nexusv1connect.AgentServiceHandler = AgentHandler{}
)

// connectErr maps vault errors to Connect codes.
func connectErr(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	switch {
	case errors.As(err, &ce):
		return err
	case errors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrInvalidPath), errors.Is(err, ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, ErrConflict):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, ErrTooLarge):
		return connect.NewError(connect.CodeResourceExhausted, err)
	case errors.Is(err, ErrNoKey):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, ErrLocked):
		return connect.NewError(connect.CodeUnavailable, err)
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func (h VaultHandler) Search(ctx context.Context, r *connect.Request[nexusv1.SearchRequest]) (*connect.Response[nexusv1.SearchResponse], error) {
	hits, err := h.V.Search(ctx, r.Msg.GetQuery(), r.Msg.GetTypes(), int(r.Msg.GetLimit()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.SearchResponse{Hits: hits}), nil
}

func (h VaultHandler) Ask(ctx context.Context, r *connect.Request[nexusv1.AskRequest]) (*connect.Response[nexusv1.AskResponse], error) {
	answer, sources, err := h.V.Ask(ctx, r.Msg.GetQuestion())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.AskResponse{Answer: answer, Sources: sources}), nil
}

func (h VaultHandler) GetNote(ctx context.Context, r *connect.Request[nexusv1.GetNoteRequest]) (*connect.Response[nexusv1.GetNoteResponse], error) {
	key := r.Msg.GetId()
	if key == "" {
		key = r.Msg.GetPath()
	}
	if key == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("id or path is required"))
	}
	n, err := h.V.Read(ctx, key)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.GetNoteResponse{Note: n}), nil
}

func (h VaultHandler) WriteNote(ctx context.Context, r *connect.Request[nexusv1.WriteNoteRequest]) (*connect.Response[nexusv1.WriteNoteResponse], error) {
	res, err := h.V.WriteNoteEx(ctx, r.Msg.GetPath(), []byte(r.Msg.GetMarkdown()), r.Msg.GetCommitMessage(), r.Msg.GetBaseCommit())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.WriteNoteResponse{Note: res.Note, Merged: res.Merged}), nil
}

func (h VaultHandler) Capture(ctx context.Context, r *connect.Request[nexusv1.CaptureRequest]) (*connect.Response[nexusv1.CaptureResponse], error) {
	ref, err := h.V.Capture(ctx, r.Msg)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.CaptureResponse{Note: ref}), nil
}

func (h VaultHandler) Graph(ctx context.Context, r *connect.Request[nexusv1.GraphRequest]) (*connect.Response[nexusv1.GraphResponse], error) {
	g, err := h.V.Graph(ctx, r.Msg)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(g), nil
}

func (h VaultHandler) ListFacts(ctx context.Context, r *connect.Request[nexusv1.ListFactsRequest]) (*connect.Response[nexusv1.ListFactsResponse], error) {
	facts, next, err := h.V.ListFacts(ctx, r.Msg.GetStatus(), r.Msg.GetPage())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.ListFactsResponse{Facts: facts, NextPageToken: next}), nil
}

func (h VaultHandler) ReviewFact(ctx context.Context, r *connect.Request[nexusv1.ReviewFactRequest]) (*connect.Response[nexusv1.ReviewFactResponse], error) {
	f, err := h.V.ReviewFact(ctx, r.Msg.GetId(), r.Msg.GetStatus(), r.Msg.GetCorrectedObject())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.ReviewFactResponse{Fact: f}), nil
}

func (h AgentHandler) ListAgents(ctx context.Context, r *connect.Request[nexusv1.ListAgentsRequest]) (*connect.Response[nexusv1.ListAgentsResponse], error) {
	as, err := h.C.ListAgents(ctx, r.Msg.GetIncludeDisabled())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.ListAgentsResponse{Agents: as}), nil
}

func (h AgentHandler) GetAgent(ctx context.Context, r *connect.Request[nexusv1.GetAgentRequest]) (*connect.Response[nexusv1.GetAgentResponse], error) {
	a, _, md, err := h.C.GetAgent(ctx, r.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.GetAgentResponse{Agent: a, Markdown: md}), nil
}

func (h AgentHandler) UpsertAgent(ctx context.Context, r *connect.Request[nexusv1.UpsertAgentRequest]) (*connect.Response[nexusv1.UpsertAgentResponse], error) {
	a, err := h.C.UpsertAgent(ctx, r.Msg.GetMarkdown(), r.Msg.GetCommitMessage())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.UpsertAgentResponse{Agent: a}), nil
}
