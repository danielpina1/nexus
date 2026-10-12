package notify

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// FollowTaskMarker is Notice.data.type of TaskService.FollowTask markers
// (handled by the Live Activity manager, never shown as a notification).
const FollowTaskMarker = "follow_task"

// maxTracked bounds the composer's dedupe maps.
const maxTracked = 10000

// Composer maps events to notifications (IOS_LIVE.md §3.1). It is not
// safe for concurrent Compose calls from multiple goroutines without its
// internal lock (it has one).
type Composer struct {
	Now          func() time.Time
	OfflineAfter time.Duration
	// AgentName resolves an agent id to its display name (optional).
	AgentName func(ctx context.Context, id string) string

	mu        sync.Mutex
	approvals map[string]nexusv1.ApprovalState
	terminal  map[string]bool
	questions map[string]bool
	nodes     map[string]*nodeState
}

type nodeState struct {
	online       bool
	offlineSince time.Time
	alerted      bool
}

func (c *Composer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Composer) init() {
	if c.approvals == nil || len(c.approvals) > maxTracked {
		c.approvals = map[string]nexusv1.ApprovalState{}
	}
	if c.terminal == nil || len(c.terminal) > maxTracked {
		c.terminal = map[string]bool{}
	}
	if c.questions == nil || len(c.questions) > maxTracked {
		c.questions = map[string]bool{}
	}
	if c.nodes == nil {
		c.nodes = map[string]*nodeState{}
	}
	if c.OfflineAfter <= 0 {
		c.OfflineAfter = 2 * time.Minute
	}
}

func (c *Composer) agentName(ctx context.Context, id string) string {
	if id == "" || c.AgentName == nil {
		return ""
	}
	return c.AgentName(ctx, id)
}

// IsFollowMarker reports whether n is a FollowTask marker notice.
func IsFollowMarker(n *nexusv1.Notice) bool {
	if n == nil || n.Data == nil {
		return false
	}
	v, ok := n.Data.Fields["type"]
	return ok && v.GetStringValue() == FollowTaskMarker
}

// TaskTerminal reports whether s is final.
func TaskTerminal(s nexusv1.TaskState) bool {
	switch s {
	case nexusv1.TaskState_TASK_STATE_SUCCEEDED, nexusv1.TaskState_TASK_STATE_FAILED, nexusv1.TaskState_TASK_STATE_CANCELLED:
		return true
	}
	return false
}

func taskTitle(t *nexusv1.Task) string {
	switch {
	case t.GetTitle() != "":
		return t.GetTitle()
	case t.GetPrompt() != "":
		return truncate(strings.TrimSpace(t.GetPrompt()), 80)
	default:
		return t.GetId()
	}
}

// Compose returns the notifications for one event (usually 0 or 1).
func (c *Composer) Compose(ctx context.Context, ev *nexusv1.Event) []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	switch b := ev.GetBody().(type) {
	case *nexusv1.Event_Approval:
		return c.approval(ctx, ev, b.Approval)
	case *nexusv1.Event_Question:
		return c.question(ctx, ev, b.Question)
	case *nexusv1.Event_TaskUpdated:
		return c.task(ctx, b.TaskUpdated.GetTask())
	case *nexusv1.Event_Notice:
		return c.notice(ev, b.Notice)
	case *nexusv1.Event_NodeStatus:
		return c.node(b.NodeStatus)
	}
	return nil
}

func (c *Composer) approval(ctx context.Context, ev *nexusv1.Event, a *nexusv1.Approval) []Message {
	if a == nil || a.Id == "" {
		return nil
	}
	if prev, seen := c.approvals[a.Id]; seen && prev == a.State {
		return nil
	}
	c.approvals[a.Id] = a.State
	taskID := a.TaskId
	if taskID == "" {
		taskID = ev.GetTaskId()
	}
	title := a.Title
	if title == "" {
		title = "Approval needed"
	}
	m := Message{
		ThreadID:   taskID,
		CollapseID: a.Id,
		TaskID:     taskID,
		ApprovalID: a.Id,
		AgentID:    a.AgentId,
		AgentName:  c.agentName(ctx, a.AgentId),
	}
	switch a.State {
	case nexusv1.ApprovalState_APPROVAL_STATE_PENDING:
		m.Category = CatApproval
		m.Body = a.Summary
		if a.Level == nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC {
			m.Category = CatApprovalBiometric
			if m.Body == "" {
				m.Body = "Review with Face ID"
			}
		}
		if m.Body == "" {
			m.Body = "Tap to review"
		}
		m.Title = title
		m.Subtitle = m.AgentName
		m.Interruption = LevelTimeSensitive
		if a.ExpiresAt != nil {
			m.Expiration = a.ExpiresAt.AsTime()
		}
		for _, p := range a.Previews {
			if p.GetUrl() != "" {
				m.AttachmentURL = p.GetUrl()
				break
			}
		}
	case nexusv1.ApprovalState_APPROVAL_STATE_APPROVED:
		m.Title, m.Body = "✓ Approved", title
	case nexusv1.ApprovalState_APPROVAL_STATE_DENIED:
		m.Title, m.Body = "✗ Denied", title
	case nexusv1.ApprovalState_APPROVAL_STATE_EXPIRED:
		m.Title, m.Body = "Expired", title
	default:
		return nil
	}
	if a.State != nexusv1.ApprovalState_APPROVAL_STATE_PENDING {
		// Replaces the pending notification (same collapse id); no actions.
		m.Interruption = LevelPassive
	}
	return []Message{m}
}

func (c *Composer) question(ctx context.Context, ev *nexusv1.Event, q *nexusv1.Question) []Message {
	if q == nil {
		return nil
	}
	if q.QuestionId != "" {
		if c.questions[q.QuestionId] {
			return nil
		}
		c.questions[q.QuestionId] = true
	}
	agentID := ""
	if strings.HasPrefix(ev.GetActor(), "agent:") {
		agentID = strings.TrimPrefix(ev.GetActor(), "agent:")
	}
	name := c.agentName(ctx, agentID)
	title := name
	if title == "" {
		title = "Question"
	}
	return []Message{{
		Category:     CatQuestion,
		Title:        title,
		Body:         q.Text,
		ThreadID:     ev.GetTaskId(),
		CollapseID:   q.QuestionId,
		TaskID:       ev.GetTaskId(),
		QuestionID:   q.QuestionId,
		AgentID:      agentID,
		AgentName:    name,
		Choices:      q.Choices,
		Interruption: LevelActive,
	}}
}

func (c *Composer) task(ctx context.Context, t *nexusv1.Task) []Message {
	if t == nil || !TaskTerminal(t.State) || t.ParentId != "" {
		return nil
	}
	if c.terminal[t.Id] {
		return nil
	}
	c.terminal[t.Id] = true
	m := Message{
		Category:   CatResult,
		Title:      taskTitle(t),
		Body:       t.Summary,
		ThreadID:   t.Id,
		CollapseID: "result-" + t.Id,
		TaskID:     t.Id,
		AgentID:    t.AgentId,
		AgentName:  c.agentName(ctx, t.AgentId),
	}
	switch t.State {
	case nexusv1.TaskState_TASK_STATE_SUCCEEDED:
		m.Subtitle = "✓ Done"
		m.Interruption = LevelActive
	case nexusv1.TaskState_TASK_STATE_FAILED:
		// Failures need attention but are not urgent: active, not time-sensitive.
		m.Subtitle = "✗ Failed"
		m.Interruption = LevelActive
	default:
		m.Subtitle = "Cancelled"
		m.Interruption = LevelPassive
	}
	if m.Body == "" {
		m.Body = m.Subtitle
	}
	return []Message{m}
}

func (c *Composer) notice(ev *nexusv1.Event, n *nexusv1.Notice) []Message {
	if n == nil || IsFollowMarker(n) {
		return nil
	}
	m := Message{
		Title:      n.Title,
		Body:       n.Body,
		ThreadID:   ev.GetTaskId(),
		CollapseID: n.Id,
		TaskID:     ev.GetTaskId(),
	}
	switch n.Kind {
	case nexusv1.NoticeKind_NOTICE_KIND_RESULT:
		m.Category, m.Interruption = CatResult, LevelActive
	case nexusv1.NoticeKind_NOTICE_KIND_ALERT:
		m.Category, m.Interruption = CatAlert, LevelTimeSensitive
	case nexusv1.NoticeKind_NOTICE_KIND_BRIEF:
		m.Category, m.Interruption = CatBrief, LevelPassive
	case nexusv1.NoticeKind_NOTICE_KIND_FACT_REVIEW:
		m.Category, m.Interruption = CatFactReview, LevelPassive
	case nexusv1.NoticeKind_NOTICE_KIND_SUGGESTION:
		m.Category, m.Interruption = CatSuggestion, LevelPassive
	default:
		m.Category, m.Interruption = CatResult, LevelPassive
	}
	if n.Data != nil {
		if u := n.Data.Fields["attachment_url"].GetStringValue(); u != "" {
			m.AttachmentURL = u
		}
	}
	if m.Title == "" {
		m.Title = "Nexus"
	}
	return []Message{m}
}

func (c *Composer) node(s *nexusv1.NodeStatus) []Message {
	if s == nil || s.NodeId == "" {
		return nil
	}
	st, known := c.nodes[s.NodeId]
	if !known {
		// First sighting: remember the state; never alert on it (a zero
		// offlineSince is skipped by Tick).
		c.nodes[s.NodeId] = &nodeState{online: s.Online}
		return nil
	}
	if s.Online {
		was := st.alerted
		st.online, st.alerted = true, false
		if was {
			// No category: the iOS service extension forces ALERT to
			// time-sensitive, and this replacement must stay passive.
			return []Message{{
				Title:        "Node back online",
				Body:         s.NodeId + " is back online.",
				CollapseID:   "node-" + s.NodeId,
				Interruption: LevelPassive,
			}}
		}
		return nil
	}
	if st.online {
		st.online, st.offlineSince, st.alerted = false, c.now(), false
	}
	return nil
}

// Tick emits debounced "node offline" alerts whose delay has elapsed.
func (c *Composer) Tick() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	now := c.now()
	var out []Message
	for id, st := range c.nodes {
		if st.online || st.alerted || st.offlineSince.IsZero() {
			continue
		}
		if now.Sub(st.offlineSince) < c.OfflineAfter {
			continue
		}
		st.alerted = true
		out = append(out, Message{
			Category:     CatAlert,
			Title:        "Node offline",
			Body:         fmt.Sprintf("%s has been offline for %s.", id, now.Sub(st.offlineSince).Round(time.Minute)),
			CollapseID:   "node-" + id,
			Interruption: LevelTimeSensitive,
		})
	}
	return out
}
