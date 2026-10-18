package architect

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

func sleepCh() <-chan time.Time { return time.After(2 * time.Second) }

func parseClass(s string) nexusv1.ActionClass {
	n := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", "_"))
	if !strings.HasPrefix(n, "ACTION_CLASS_") {
		n = "ACTION_CLASS_" + n
	}
	if v, ok := nexusv1.ActionClass_value[n]; ok && v != 0 {
		return nexusv1.ActionClass(v)
	}
	return nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL
}

func parseLevel(s string) nexusv1.TrustLevel {
	n := strings.ToUpper(strings.TrimSpace(s))
	if n == "" {
		return nexusv1.TrustLevel_TRUST_LEVEL_UNSPECIFIED
	}
	if !strings.HasPrefix(n, "TRUST_LEVEL_") {
		n = "TRUST_LEVEL_" + n
	}
	return nexusv1.TrustLevel(nexusv1.TrustLevel_value[n])
}

// RequestPublish re-validates a ready draft and asks the owner to approve
// writing its notes. Nothing is written until the approval is granted:
// agents can never write agents/ (the vault tools refuse it), so the
// architect writes through the owner's catalog/vault services, and only
// with the owner's explicit decision on the exact notes (the payload).
func (s *Service) RequestPublish(ctx context.Context, id string) (*nexusv1.AgentDraft, *nexusv1.Approval, error) {
	broker, ok := lookup[kernel.ApprovalBroker](s.h, kernel.SvcApprovals)
	if !ok {
		return nil, nil, connect.NewError(connect.CodeUnavailable, errors.New("publishing needs the approvals module"))
	}
	d, err := s.load(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if d.State != stReady {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("draft is %s; only a ready draft can be published", stateName(d.State)))
	}
	// Re-validate: the catalog or the tool registry may have changed.
	res := Validate(s.BuildEnv(ctx, d.UpdateAgentId), d.Notes)
	if len(res.Problems) > 0 {
		d, _ = s.update(ctx, id, func(d *nexusv1.AgentDraft) error {
			d.Notes, d.Problems, d.State = res.Notes, res.Problems, stInvalid
			return nil
		})
		return d, nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("draft is invalid: %s", strings.Join(res.Problems, "; ")))
	}
	var notes []any
	var ids []string
	for _, n := range res.Notes {
		notes = append(notes, map[string]any{"kind": n.Kind, "id": n.Id, "path": n.Path, "exists": n.Exists,
			"markdown": n.Markdown, "diff": n.Diff})
		ids = append(ids, n.Kind+" "+n.Id)
	}
	payload, err := structpb.NewStruct(map[string]any{"draft_id": d.Id, "revision": float64(d.Revision), "notes": notes})
	if err != nil {
		return nil, nil, err
	}
	summary := d.Rationale
	if len(d.Risks) > 0 {
		summary += "\nRisks: " + strings.Join(d.Risks, "; ")
	}
	a, err := broker.Request(ctx, &nexusv1.Approval{
		ActionClass: parseClass(s.cfg.PublishClass),
		Level:       parseLevel(s.cfg.PublishLevel),
		Title:       "Publish " + strings.Join(ids, ", "),
		Summary:     strings.TrimSpace(summary),
		Payload:     payload,
	})
	if err != nil {
		return nil, nil, err
	}
	d, err = s.update(ctx, id, func(d *nexusv1.AgentDraft) error {
		if d.State != stReady {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("draft is %s", stateName(d.State)))
		}
		d.Notes, d.ApprovalId, d.State, d.Error = res.Notes, a.Id, stPending, ""
		return nil
	})
	return d, a, err
}

// OnApproval reacts to a decided approval of a pending publish.
func (s *Service) OnApproval(ctx context.Context, a *nexusv1.Approval) error {
	if a.GetId() == "" {
		return nil
	}
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM architect_drafts WHERE approval_id = ? AND state = ?`, a.Id, int32(stPending)).Scan(&id); err != nil {
		return nil // not ours, or already handled
	}
	switch a.State {
	case nexusv1.ApprovalState_APPROVAL_STATE_APPROVED:
		return s.write(ctx, id, a.Id)
	case nexusv1.ApprovalState_APPROVAL_STATE_DENIED, nexusv1.ApprovalState_APPROVAL_STATE_EXPIRED:
		_, err := s.update(ctx, id, func(d *nexusv1.AgentDraft) error {
			if d.State == stPending && d.ApprovalId == a.Id {
				d.State = stReady
				d.Error = "publishing was " + strings.ToLower(strings.TrimPrefix(a.State.String(), "APPROVAL_STATE_")) + " by the owner"
			}
			return nil
		})
		return err
	}
	return nil
}

// write publishes the approved notes through the owner's services.
func (s *Service) write(ctx context.Context, id, approvalID string) error {
	d, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if d.State != stPending || d.ApprovalId != approvalID {
		return nil
	}
	msg := fmt.Sprintf("architect: publish draft %s (approval %s)", d.Id, approvalID)
	var werr error
	for _, n := range d.Notes { // all or nothing: check every writer first
		if !s.canWrite(n) {
			werr = fmt.Errorf("%s %s: no vault/catalog service to write %s", n.Kind, n.Id, n.Path)
		}
	}
	for _, n := range d.Notes {
		if werr != nil {
			break
		}
		if werr = s.writeNote(ctx, n, msg); werr != nil {
			werr = fmt.Errorf("%s %s: %w", n.Kind, n.Id, werr)
			break
		}
	}
	d, err = s.update(ctx, id, func(d *nexusv1.AgentDraft) error {
		if werr != nil {
			d.State, d.Error = stReady, "publish failed: "+werr.Error()
		} else {
			d.State, d.Error = stPublished, ""
		}
		return nil
	})
	if err != nil {
		return err
	}
	if werr != nil {
		s.notice(ctx, d, "Publishing "+draftTitle(d)+" failed", werr.Error())
		return werr
	}
	s.notice(ctx, d, "Agent published: "+draftTitle(d), "Written to "+notePaths(d))
	return nil
}

func notePaths(d *nexusv1.AgentDraft) string {
	var p []string
	for _, n := range d.Notes {
		p = append(p, n.Path)
	}
	return strings.Join(p, ", ")
}

func (s *Service) canWrite(n *nexusv1.DraftNote) bool {
	if _, ok := lookup[kernel.Vault](s.h, kernel.SvcVault); ok {
		return true
	}
	switch n.Kind {
	case "agent":
		_, ok := lookup[kernel.AgentCatalog](s.h, kernel.SvcAgents)
		return ok
	case "team":
		_, ok := lookup[kernel.TeamCatalog](s.h, kernel.SvcTeams)
		return ok
	}
	return false
}

func (s *Service) writeNote(ctx context.Context, n *nexusv1.DraftNote, msg string) error {
	switch n.Kind {
	case "agent":
		if cat, ok := lookup[kernel.AgentCatalog](s.h, kernel.SvcAgents); ok {
			_, err := cat.UpsertAgent(ctx, n.Markdown, msg)
			return err
		}
	case "team":
		if tc, ok := lookup[kernel.TeamCatalog](s.h, kernel.SvcTeams); ok {
			_, err := tc.UpsertTeam(ctx, n.Markdown, msg, false)
			return err
		}
	}
	v, ok := lookup[kernel.Vault](s.h, kernel.SvcVault)
	if !ok {
		return errors.New("no vault/catalog service to write " + n.Path)
	}
	_, err := v.Write(ctx, n.Path, n.Markdown, msg)
	return err
}

// Start watches approval decisions and settles drafts interrupted by a
// restart.
func (s *Service) Start(ctx context.Context) error {
	for _, d := range mustList(s.list(ctx, []nexusv1.DraftState{stDrafting, stTesting}, 500)) {
		_, _ = s.update(ctx, d.Id, func(d *nexusv1.AgentDraft) error {
			if d.State == stTesting {
				d.State = stReady
				if len(d.Problems) > 0 {
					d.State = stInvalid
				}
			} else {
				d.State, d.Error = stFailed, "the hub restarted while drafting; draft again or refine"
			}
			return nil
		})
	}
	// Approvals decided while the hub was down.
	if broker, ok := lookup[kernel.ApprovalBroker](s.h, kernel.SvcApprovals); ok {
		pending := mustList(s.list(ctx, []nexusv1.DraftState{stPending}, 500))
		if len(pending) > 0 {
			if all, err := broker.List(ctx, nil); err == nil {
				byID := map[string]*nexusv1.Approval{}
				for _, a := range all {
					byID[a.Id] = a
				}
				for _, d := range pending {
					if a := byID[d.ApprovalId]; a != nil && a.State != nexusv1.ApprovalState_APPROVAL_STATE_PENDING {
						if err := s.OnApproval(ctx, a); err != nil {
							s.log.Warn("architect: settle approval", "draft", d.Id, "err", err)
						}
					}
				}
			}
		}
	}
	bus, ok := lookup[kernel.EventBus](s.h, kernel.SvcEventBus)
	if !ok {
		return nil
	}
	since, _ := bus.LastSeq(ctx)
	ch, err := bus.Subscribe(s.bgCtx, since, kernel.EventFilter{InboxOnly: true})
	if err != nil {
		return err
	}
	s.goBG(func(ctx context.Context) {
		for ev := range ch {
			if a := ev.GetApproval(); a != nil && a.State != nexusv1.ApprovalState_APPROVAL_STATE_PENDING {
				if err := s.OnApproval(ctx, a); err != nil {
					s.log.Warn("architect: approval", "approval", a.Id, "err", err)
				}
			}
		}
	})
	return nil
}

func mustList(l []*nexusv1.AgentDraft, _ error) []*nexusv1.AgentDraft { return l }
