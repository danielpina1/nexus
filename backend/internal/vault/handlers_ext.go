package vault

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// Handlers for the robustness and feature RPCs (see docs/VAULT.md §11).

func idOrPath(id, path string) (string, error) {
	if id != "" {
		return id, nil
	}
	if path != "" {
		return path, nil
	}
	return "", connect.NewError(connect.CodeInvalidArgument, errors.New("id or path is required"))
}

func (h VaultHandler) History(ctx context.Context, r *connect.Request[nexusv1.HistoryRequest]) (*connect.Response[nexusv1.HistoryResponse], error) {
	key, err := idOrPath(r.Msg.GetId(), r.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	vs, err := h.V.History(ctx, key, int(r.Msg.GetLimit()), r.Msg.GetIncludeDiff())
	if err != nil {
		return nil, connectErr(err)
	}
	out := &nexusv1.HistoryResponse{}
	for _, v := range vs {
		out.Versions = append(out.Versions, v.Proto())
	}
	return connect.NewResponse(out), nil
}

func (h VaultHandler) GetVersion(ctx context.Context, r *connect.Request[nexusv1.GetVersionRequest]) (*connect.Response[nexusv1.GetVersionResponse], error) {
	key, err := idOrPath(r.Msg.GetId(), r.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	b, err := h.V.GetVersion(ctx, key, r.Msg.GetCommit())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.GetVersionResponse{Markdown: string(b), Version: &nexusv1.NoteVersion{Commit: r.Msg.GetCommit()}}), nil
}

func (h VaultHandler) RestoreVersion(ctx context.Context, r *connect.Request[nexusv1.RestoreVersionRequest]) (*connect.Response[nexusv1.RestoreVersionResponse], error) {
	key, err := idOrPath(r.Msg.GetId(), r.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	n, err := h.V.RestoreVersion(ctx, key, r.Msg.GetCommit(), r.Msg.GetCommitMessage())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.RestoreVersionResponse{Note: n}), nil
}

func (h VaultHandler) DeleteNote(ctx context.Context, r *connect.Request[nexusv1.DeleteNoteRequest]) (*connect.Response[nexusv1.DeleteNoteResponse], error) {
	key, err := idOrPath(r.Msg.GetId(), r.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	it, err := h.V.Delete(ctx, key, r.Msg.GetBaseCommit(), r.Msg.GetCommitMessage())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.DeleteNoteResponse{Item: it.Proto()}), nil
}

func (h VaultHandler) ListTrash(ctx context.Context, _ *connect.Request[nexusv1.ListTrashRequest]) (*connect.Response[nexusv1.ListTrashResponse], error) {
	items, err := h.V.ListTrash(ctx)
	if err != nil {
		return nil, connectErr(err)
	}
	out := &nexusv1.ListTrashResponse{}
	for _, it := range items {
		out.Items = append(out.Items, it.Proto())
	}
	return connect.NewResponse(out), nil
}

func (h VaultHandler) RestoreFromTrash(ctx context.Context, r *connect.Request[nexusv1.RestoreFromTrashRequest]) (*connect.Response[nexusv1.RestoreFromTrashResponse], error) {
	p, err := h.V.RestoreFromTrash(ctx, r.Msg.GetId(), r.Msg.GetPath())
	if err != nil {
		return nil, connectErr(err)
	}
	out := &nexusv1.RestoreFromTrashResponse{Path: p}
	if e, ok := h.V.Index().Get(ctx, p); ok {
		out.Note = entryRef(e)
	}
	return connect.NewResponse(out), nil
}

func (h VaultHandler) PurgeTrash(ctx context.Context, r *connect.Request[nexusv1.PurgeTrashRequest]) (*connect.Response[nexusv1.PurgeTrashResponse], error) {
	n, err := h.V.PurgeTrash(ctx, r.Msg.GetId(), time.Duration(r.Msg.GetOlderThanDays())*24*time.Hour)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.PurgeTrashResponse{Purged: int32(n)}), nil
}

func (h VaultHandler) RenameNote(ctx context.Context, r *connect.Request[nexusv1.RenameNoteRequest]) (*connect.Response[nexusv1.RenameNoteResponse], error) {
	res, err := h.V.Rename(ctx, r.Msg.GetFrom(), r.Msg.GetTo(), r.Msg.GetSkipLinkUpdates(), r.Msg.GetCommitMessage())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.RenameNoteResponse{Note: res.Note, UpdatedPaths: res.Updated}), nil
}

func (h VaultHandler) MergeNotes(ctx context.Context, r *connect.Request[nexusv1.MergeNotesRequest]) (*connect.Response[nexusv1.MergeNotesResponse], error) {
	res, err := h.V.Merge(ctx, r.Msg.GetSource(), r.Msg.GetTarget(), r.Msg.GetCommitMessage())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.MergeNotesResponse{Note: res.Note, UpdatedPaths: res.Updated}), nil
}

func (h VaultHandler) SplitNote(ctx context.Context, r *connect.Request[nexusv1.SplitNoteRequest]) (*connect.Response[nexusv1.SplitNoteResponse], error) {
	key, err := idOrPath(r.Msg.GetId(), r.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	res, err := h.V.Split(ctx, key, int(r.Msg.GetHeadingLevel()), r.Msg.GetFolder())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.SplitNoteResponse{Note: res.Note, Created: res.Created}), nil
}

func (h VaultHandler) Query(ctx context.Context, r *connect.Request[nexusv1.QueryRequest]) (*connect.Response[nexusv1.QueryResponse], error) {
	res, err := h.V.Query(ctx, r.Msg.GetQuery(), int(r.Msg.GetLimit()))
	if err != nil {
		return nil, connectErr(err)
	}
	out := &nexusv1.QueryResponse{Total: int32(res.Total), Warnings: res.Warnings}
	for _, hit := range res.Hits {
		e := hit.Entry
		q := &nexusv1.QueryHit{Note: entryRef(e), Type: e.Type, Tags: e.Tags, UpdatedAt: timestamppb.New(e.Updated),
			CreatedAt: timestamppb.New(e.Created), Snippet: hit.Snippet, Score: float32(hit.Score)}
		if fm, err := toStruct(e.Frontmatter); err == nil {
			q.Frontmatter = fm
		}
		out.Hits = append(out.Hits, q)
	}
	return connect.NewResponse(out), nil
}

func (h VaultHandler) Related(ctx context.Context, r *connect.Request[nexusv1.RelatedRequest]) (*connect.Response[nexusv1.RelatedResponse], error) {
	key, err := idOrPath(r.Msg.GetId(), r.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	rel, err := h.V.Related(ctx, key, int(r.Msg.GetLimit()))
	if err != nil {
		return nil, connectErr(err)
	}
	out := &nexusv1.RelatedResponse{}
	for _, x := range rel {
		out.Notes = append(out.Notes, &nexusv1.RelatedNote{Note: entryRef(x.Entry), Score: float32(x.Score), Reasons: x.Reasons})
	}
	return connect.NewResponse(out), nil
}

func (h VaultHandler) Backlinks(ctx context.Context, r *connect.Request[nexusv1.BacklinksRequest]) (*connect.Response[nexusv1.BacklinksResponse], error) {
	key, err := idOrPath(r.Msg.GetId(), r.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	links, mentions, err := h.V.Backlinks(ctx, key, r.Msg.GetIncludeUnlinked(), int(r.Msg.GetLimit()))
	if err != nil {
		return nil, connectErr(err)
	}
	out := &nexusv1.BacklinksResponse{}
	for _, b := range links {
		out.Backlinks = append(out.Backlinks, b.Proto())
	}
	for _, b := range mentions {
		out.UnlinkedMentions = append(out.UnlinkedMentions, b.Proto())
	}
	return connect.NewResponse(out), nil
}

func (h VaultHandler) GraphStats(ctx context.Context, r *connect.Request[nexusv1.GraphStatsRequest]) (*connect.Response[nexusv1.GraphStatsResponse], error) {
	st, err := h.V.Stats(ctx, int(r.Msg.GetLimit()))
	if err != nil {
		return nil, connectErr(err)
	}
	out := &nexusv1.GraphStatsResponse{Notes: int32(st.Notes), Edges: int32(st.Edges), DanglingLinks: int32(st.Dangling), TopDangling: st.TopDangling}
	for _, e := range st.Orphans {
		out.Orphans = append(out.Orphans, entryRef(e))
	}
	for _, hb := range st.Hubs {
		out.Hubs = append(out.Hubs, &nexusv1.GraphHub{Note: entryRef(hb.Entry), InDegree: int32(hb.In), OutDegree: int32(hb.Out)})
	}
	return connect.NewResponse(out), nil
}

func (h VaultHandler) ListNoteTasks(ctx context.Context, r *connect.Request[nexusv1.ListNoteTasksRequest]) (*connect.Response[nexusv1.ListNoteTasksResponse], error) {
	m := r.Msg
	ts, err := h.V.ListTasks(ctx, TaskFilter{Status: m.GetStatus(), DueBefore: m.GetDueBefore(), DueAfter: m.GetDueAfter(), Tags: m.GetTags(),
		Prefix: m.GetPathPrefix(), Note: m.GetNoteId(), Text: m.GetText(), Limit: int(m.GetLimit())})
	if err != nil {
		return nil, connectErr(err)
	}
	out := &nexusv1.ListNoteTasksResponse{}
	for _, t := range ts {
		out.Tasks = append(out.Tasks, h.V.taskProto(ctx, t))
	}
	return connect.NewResponse(out), nil
}

func (h VaultHandler) ToggleTask(ctx context.Context, r *connect.Request[nexusv1.ToggleTaskRequest]) (*connect.Response[nexusv1.ToggleTaskResponse], error) {
	key, err := idOrPath(r.Msg.GetId(), r.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	var done *bool
	if r.Msg.Done != nil {
		d := r.Msg.GetDone()
		done = &d
	}
	t, n, err := h.V.ToggleTask(ctx, key, int(r.Msg.GetLine()), r.Msg.GetRaw(), done)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.ToggleTaskResponse{Task: h.V.taskProto(ctx, *t), Commit: n.GetCommit()}), nil
}

func (h VaultHandler) ListTemplates(ctx context.Context, _ *connect.Request[nexusv1.ListTemplatesRequest]) (*connect.Response[nexusv1.ListTemplatesResponse], error) {
	ts, err := h.V.ListTemplates(ctx)
	if err != nil {
		return nil, connectErr(err)
	}
	out := &nexusv1.ListTemplatesResponse{}
	for _, t := range ts {
		out.Templates = append(out.Templates, &nexusv1.NoteTemplate{Path: t.Path, Name: t.Name, Variables: t.Variables, Prompts: t.Prompts, Type: t.Type})
	}
	return connect.NewResponse(out), nil
}

func (h VaultHandler) NewFromTemplate(ctx context.Context, r *connect.Request[nexusv1.NewFromTemplateRequest]) (*connect.Response[nexusv1.NewFromTemplateResponse], error) {
	n, err := h.V.NewFromTemplate(ctx, r.Msg.GetTemplate(), r.Msg.GetPath(), r.Msg.GetTitle(), r.Msg.GetVariables(), r.Msg.GetCommitMessage())
	var mp *MissingPromptsError
	if errors.As(err, &mp) {
		// Not an error for the client: ask the user and call again.
		return connect.NewResponse(&nexusv1.NewFromTemplateResponse{MissingPrompts: mp.Prompts}), nil
	}
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.NewFromTemplateResponse{Note: n}), nil
}

func (h VaultHandler) PeriodicNote(ctx context.Context, r *connect.Request[nexusv1.PeriodicNoteRequest]) (*connect.Response[nexusv1.PeriodicNoteResponse], error) {
	var day time.Time
	if d := r.Msg.GetDate(); d != "" {
		t, err := time.ParseInLocation("2006-01-02", d, time.Local)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("date must be YYYY-MM-DD"))
		}
		day = t
	}
	n, rel, created, err := h.V.PeriodicNote(ctx, strings.ToLower(r.Msg.GetPeriod()), day, r.Msg.GetCreate())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.PeriodicNoteResponse{Note: n, Created: created, Path: rel}), nil
}

func (h VaultHandler) Check(ctx context.Context, r *connect.Request[nexusv1.CheckRequest]) (*connect.Response[nexusv1.CheckResponse], error) {
	rep, err := h.V.Check(ctx, r.Msg.GetFull())
	if err != nil {
		return nil, connectErr(err)
	}
	h.V.setLastReport(rep)
	return connect.NewResponse(&nexusv1.CheckResponse{Report: rep.Proto()}), nil
}

func (h VaultHandler) Repair(ctx context.Context, r *connect.Request[nexusv1.RepairRequest]) (*connect.Response[nexusv1.RepairResponse], error) {
	actions, rep, err := h.V.Repair(ctx, r.Msg.GetDryRun())
	if err != nil {
		return nil, connectErr(err)
	}
	out := &nexusv1.RepairResponse{Actions: actions}
	if rep != nil {
		out.Report = rep.Proto()
	}
	return connect.NewResponse(out), nil
}

func (h VaultHandler) backupOpts() (*BackupOptions, error) {
	if h.BackupConfig == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("backups are not configured (vault.backup.dir)"))
	}
	o := h.BackupConfig()
	if o == nil || o.Dir == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("backups are not configured (vault.backup.dir)"))
	}
	return o, nil
}

func backupProto(b BackupInfo) *nexusv1.BackupInfo {
	return &nexusv1.BackupInfo{Name: b.Name, CreatedAt: timestamppb.New(b.Created), SizeBytes: b.Size, Kind: b.Kind, Sha256: b.SHA256}
}

func (h VaultHandler) Backup(ctx context.Context, r *connect.Request[nexusv1.BackupRequest]) (*connect.Response[nexusv1.BackupResponse], error) {
	o, err := h.backupOpts()
	if err != nil {
		return nil, err
	}
	opts := *o
	if !r.Msg.GetPushRemote() {
		opts.Remote = ""
	}
	res, err := h.V.Backup(ctx, opts)
	if err != nil && res == nil {
		return nil, connectErr(err)
	}
	out := &nexusv1.BackupResponse{Pruned: res.Pruned, PushResult: res.PushResult}
	if err != nil {
		out.PushResult = err.Error()
	}
	for _, b := range res.Created {
		out.Created = append(out.Created, backupProto(b))
	}
	return connect.NewResponse(out), nil
}

func (h VaultHandler) ListBackups(ctx context.Context, _ *connect.Request[nexusv1.ListBackupsRequest]) (*connect.Response[nexusv1.ListBackupsResponse], error) {
	o, err := h.backupOpts()
	if err != nil {
		return nil, err
	}
	bs, err := ListBackups(o.Dir)
	if err != nil {
		return nil, connectErr(err)
	}
	out := &nexusv1.ListBackupsResponse{Dir: o.Dir}
	for _, b := range bs {
		out.Backups = append(out.Backups, backupProto(b))
	}
	return connect.NewResponse(out), nil
}

func (h VaultHandler) VerifyBackup(ctx context.Context, r *connect.Request[nexusv1.VerifyBackupRequest]) (*connect.Response[nexusv1.VerifyBackupResponse], error) {
	o, err := h.backupOpts()
	if err != nil {
		return nil, err
	}
	name := r.Msg.GetName()
	if !reBackupName.MatchString(name) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name must be a backup file name from ListBackups"))
	}
	files, problems, err := VerifyBackup(ctx, o.Dir+"/"+name)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.VerifyBackupResponse{Ok: len(problems) == 0, Files: int32(files), Problems: problems}), nil
}

func (h VaultHandler) WatchVault(ctx context.Context, r *connect.Request[nexusv1.WatchVaultRequest], stream *connect.ServerStream[nexusv1.WatchVaultResponse]) error {
	// Flush the response headers now so clients see the stream open before
	// the first change.
	if err := stream.Send(nil); err != nil {
		return err
	}
	err := h.V.WatchChanges(ctx, r.Msg.GetSinceRev(), r.Msg.GetPathPrefix(), func(c Change) error {
		return stream.Send(&nexusv1.WatchVaultResponse{Change: c.Proto()})
	})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return connectErr(err)
}

func (h VaultHandler) ListAttachments(ctx context.Context, r *connect.Request[nexusv1.ListAttachmentsRequest]) (*connect.Response[nexusv1.ListAttachmentsResponse], error) {
	atts, err := h.V.ListAttachments(ctx, AttachmentFilter{Prefix: r.Msg.GetPathPrefix(), OrphanedOnly: r.Msg.GetOrphanedOnly(), Query: r.Msg.GetQuery(), Limit: int(r.Msg.GetLimit())})
	if err != nil {
		return nil, connectErr(err)
	}
	out := &nexusv1.ListAttachmentsResponse{}
	for _, a := range atts {
		out.Attachments = append(out.Attachments, h.V.attachmentProto(ctx, a))
	}
	return connect.NewResponse(out), nil
}

func (h VaultHandler) ImportNotes(ctx context.Context, r *connect.Request[nexusv1.ImportNotesRequest]) (*connect.Response[nexusv1.ImportNotesResponse], error) {
	src := r.Msg.GetSourceDir()
	if !strings.HasPrefix(src, "/") && !(len(src) > 2 && src[1] == ':') {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("source_dir must be an absolute path on the hub"))
	}
	res, err := h.V.ImportNotes(ctx, ImportOptions{Source: src, Dest: r.Msg.GetDestFolder(), Kind: r.Msg.GetKind(), DryRun: r.Msg.GetDryRun()})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.ImportNotesResponse{Notes: int32(res.Notes), Attachments: int32(res.Attachments), IdsAssigned: int32(res.IDsAssigned),
		IdsReplaced: int32(res.IDsReplaced), Skipped: res.Skipped, Warnings: res.Warnings}), nil
}

var _ = structpb.NewStruct
