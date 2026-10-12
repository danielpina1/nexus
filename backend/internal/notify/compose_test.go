package notify

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

func evApproval(a *nexusv1.Approval) *nexusv1.Event {
	return &nexusv1.Event{TaskId: a.TaskId, Body: &nexusv1.Event_Approval{Approval: a}}
}

func evTask(t *nexusv1.Task) *nexusv1.Event {
	return &nexusv1.Event{TaskId: t.Id, Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: t}}}
}

func evNotice(task string, n *nexusv1.Notice) *nexusv1.Event {
	return &nexusv1.Event{TaskId: task, Body: &nexusv1.Event_Notice{Notice: n}}
}

func evNode(id string, online bool) *nexusv1.Event {
	return &nexusv1.Event{Body: &nexusv1.Event_NodeStatus{NodeStatus: &nexusv1.NodeStatus{NodeId: id, Online: online}}}
}

func mustStruct(m map[string]any) *structpb.Struct {
	s, err := structpb.NewStruct(m)
	if err != nil {
		panic(err)
	}
	return s
}

func TestComposeMapping(t *testing.T) {
	exp := time.Unix(1760003600, 0)
	pending := &nexusv1.Approval{Id: "apr_1", TaskId: "tsk_1", AgentId: "shopper", Title: "Checkout €84.30",
		Summary: "22 items", State: nexusv1.ApprovalState_APPROVAL_STATE_PENDING, Level: nexusv1.TrustLevel_TRUST_LEVEL_APPROVE,
		ExpiresAt: timestamppb.New(exp), Previews: []*nexusv1.Attachment{{Url: "https://hub/blob/1.png"}}}
	bio := &nexusv1.Approval{Id: "apr_2", TaskId: "tsk_1", Title: "Push to main",
		State: nexusv1.ApprovalState_APPROVAL_STATE_PENDING, Level: nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC}
	type want struct {
		cat, title, body, level, collapse, thread, approval, question string
		choices                                                       int
	}
	cases := []struct {
		name string
		ev   *nexusv1.Event
		want *want
	}{
		{"approval pending", evApproval(pending), &want{cat: CatApproval, title: "Checkout €84.30", body: "22 items",
			level: LevelTimeSensitive, collapse: "apr_1", thread: "tsk_1", approval: "apr_1"}},
		{"approval biometric", evApproval(bio), &want{cat: CatApprovalBiometric, title: "Push to main", body: "Review with Face ID",
			level: LevelTimeSensitive, collapse: "apr_2", thread: "tsk_1", approval: "apr_2"}},
		{"approval approved", evApproval(&nexusv1.Approval{Id: "apr_1", TaskId: "tsk_1", Title: "Checkout €84.30",
			State: nexusv1.ApprovalState_APPROVAL_STATE_APPROVED}),
			&want{title: "✓ Approved", body: "Checkout €84.30", level: LevelPassive, collapse: "apr_1", thread: "tsk_1", approval: "apr_1"}},
		{"approval approved duplicate", evApproval(&nexusv1.Approval{Id: "apr_1", TaskId: "tsk_1",
			State: nexusv1.ApprovalState_APPROVAL_STATE_APPROVED}), nil},
		{"approval denied", evApproval(&nexusv1.Approval{Id: "apr_2", TaskId: "tsk_1", Title: "Push to main",
			State: nexusv1.ApprovalState_APPROVAL_STATE_DENIED}),
			&want{title: "✗ Denied", body: "Push to main", level: LevelPassive, collapse: "apr_2", thread: "tsk_1", approval: "apr_2"}},
		{"approval expired", evApproval(&nexusv1.Approval{Id: "apr_3", TaskId: "tsk_9", Title: "X",
			State: nexusv1.ApprovalState_APPROVAL_STATE_EXPIRED}),
			&want{title: "Expired", body: "X", level: LevelPassive, collapse: "apr_3", thread: "tsk_9", approval: "apr_3"}},
		{"question", &nexusv1.Event{TaskId: "tsk_1", Actor: "agent:shopper", Body: &nexusv1.Event_Question{Question: &nexusv1.Question{
			QuestionId: "q_1", Text: "Which brand?", Choices: []string{"A", "B"}}}},
			&want{cat: CatQuestion, title: "Shopper", body: "Which brand?", level: LevelActive, collapse: "q_1", thread: "tsk_1", question: "q_1", choices: 2}},
		{"task running ignored", evTask(&nexusv1.Task{Id: "tsk_1", State: nexusv1.TaskState_TASK_STATE_RUNNING}), nil},
		{"child task ignored", evTask(&nexusv1.Task{Id: "tsk_c", ParentId: "tsk_1", State: nexusv1.TaskState_TASK_STATE_SUCCEEDED}), nil},
		{"root succeeded", evTask(&nexusv1.Task{Id: "tsk_1", Title: "Groceries", Summary: "Ordered", State: nexusv1.TaskState_TASK_STATE_SUCCEEDED}),
			&want{cat: CatResult, title: "Groceries", body: "Ordered", level: LevelActive, collapse: "result-tsk_1", thread: "tsk_1"}},
		{"root succeeded duplicate", evTask(&nexusv1.Task{Id: "tsk_1", State: nexusv1.TaskState_TASK_STATE_SUCCEEDED}), nil},
		{"root failed", evTask(&nexusv1.Task{Id: "tsk_2", Prompt: "fix tests", State: nexusv1.TaskState_TASK_STATE_FAILED}),
			&want{cat: CatResult, title: "fix tests", body: "✗ Failed", level: LevelActive, collapse: "result-tsk_2", thread: "tsk_2"}},
		{"root cancelled", evTask(&nexusv1.Task{Id: "tsk_3", Title: "t", State: nexusv1.TaskState_TASK_STATE_CANCELLED}),
			&want{cat: CatResult, title: "t", body: "Cancelled", level: LevelPassive, collapse: "result-tsk_3", thread: "tsk_3"}},
		{"follow marker skipped", evNotice("tsk_1", &nexusv1.Notice{Id: "ntc_0", Title: "Following task",
			Data: mustStruct(map[string]any{"type": "follow_task"})}), nil},
		{"notice result", evNotice("tsk_1", &nexusv1.Notice{Id: "ntc_1", Kind: nexusv1.NoticeKind_NOTICE_KIND_RESULT, Title: "R", Body: "b"}),
			&want{cat: CatResult, title: "R", body: "b", level: LevelActive, collapse: "ntc_1", thread: "tsk_1"}},
		{"notice alert", evNotice("", &nexusv1.Notice{Id: "ntc_2", Kind: nexusv1.NoticeKind_NOTICE_KIND_ALERT, Title: "Disk full"}),
			&want{cat: CatAlert, title: "Disk full", level: LevelTimeSensitive, collapse: "ntc_2"}},
		{"notice brief", evNotice("", &nexusv1.Notice{Id: "ntc_3", Kind: nexusv1.NoticeKind_NOTICE_KIND_BRIEF, Title: "Morning"}),
			&want{cat: CatBrief, title: "Morning", level: LevelPassive, collapse: "ntc_3"}},
		{"notice fact review", evNotice("", &nexusv1.Notice{Id: "ntc_4", Kind: nexusv1.NoticeKind_NOTICE_KIND_FACT_REVIEW, Title: "F"}),
			&want{cat: CatFactReview, title: "F", level: LevelPassive, collapse: "ntc_4"}},
		{"notice suggestion", evNotice("", &nexusv1.Notice{Id: "ntc_5", Kind: nexusv1.NoticeKind_NOTICE_KIND_SUGGESTION, Title: "S"}),
			&want{cat: CatSuggestion, title: "S", level: LevelPassive, collapse: "ntc_5"}},
		{"run output ignored", &nexusv1.Event{TaskId: "tsk_1", Body: &nexusv1.Event_RunOutput{RunOutput: &nexusv1.RunOutput{Text: "x"}}}, nil},
	}
	c := &Composer{AgentName: func(_ context.Context, id string) string {
		if id == "shopper" {
			return "Shopper"
		}
		return ""
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.Compose(context.Background(), tc.ev)
			if tc.want == nil {
				if len(got) != 0 {
					t.Fatalf("want none, got %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("want 1 message, got %d", len(got))
			}
			m, w := got[0], tc.want
			if m.Category != w.cat || m.Title != w.title || (w.body != "" && m.Body != w.body) || m.level() != w.level ||
				m.CollapseID != w.collapse || m.ThreadID != w.thread || m.ApprovalID != w.approval ||
				m.QuestionID != w.question || len(m.Choices) != w.choices {
				t.Errorf("got %+v (level %s), want %+v", m, m.level(), *w)
			}
		})
	}
	// Pending approval extras.
	m := (&Composer{}).Compose(context.Background(), evApproval(pending))[0]
	if !m.Expiration.Equal(exp) || m.AttachmentURL != "https://hub/blob/1.png" {
		t.Errorf("expiration/attachment: %+v", m)
	}
}

func TestNodeOfflineDebounce(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1760000000, 0)}
	c := &Composer{Now: clk.Now, OfflineAfter: 2 * time.Minute}
	ctx := context.Background()
	steps := []struct {
		ev       *nexusv1.Event
		advance  time.Duration
		want     string // title from Compose or Tick, "" = none
		fromTick bool
	}{
		{ev: evNode("mac", false)},                  // first sighting offline: silent
		{advance: 10 * time.Minute, fromTick: true}, // still silent
		{ev: evNode("mac", true)},                   // online
		{ev: evNode("mac", false)},                  // goes offline
		{advance: time.Minute, fromTick: true},      // within debounce
		{ev: evNode("mac", true)},                   // flap back: no alert
		{ev: evNode("mac", false)},                  // offline again
		{advance: 3 * time.Minute, fromTick: true, want: "Node offline"},
		{advance: 3 * time.Minute, fromTick: true}, // only once
		{ev: evNode("mac", true), want: "Node back online"},
	}
	for i, s := range steps {
		clk.Advance(s.advance)
		var got []Message
		if s.fromTick {
			got = c.Tick()
		} else {
			got = c.Compose(ctx, s.ev)
		}
		title := ""
		if len(got) > 0 {
			title = got[0].Title
			if got[0].CollapseID != "node-mac" || (s.fromTick && got[0].Category != CatAlert) {
				t.Errorf("step %d: %+v", i, got[0])
			}
		}
		if title != s.want {
			t.Errorf("step %d: got %q want %q", i, title, s.want)
		}
	}
}

func TestFromNotification(t *testing.T) {
	m := FromNotification(kernelNotification())
	if m.Interruption != LevelTimeSensitive || m.TaskID != "tsk_1" || m.ApprovalID != "apr_1" ||
		len(m.Choices) != 2 || m.Subtitle != "sub" || len(m.Items) != 1 || m.Items[0].Name != "Milk" {
		t.Errorf("%+v", m)
	}
}
