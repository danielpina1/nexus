package notify

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/security"
)

// Activity types (RunActivityAttributes.activityType / PulseActivityAttributes.activityType).
const (
	ActivityRun   = "run"
	ActivityPulse = "pulse"
)

// Swift type names for push-to-start `attributes-type`.
const (
	RunAttributesType   = "RunActivityAttributes"
	PulseAttributesType = "PulseActivityAttributes"
)

// Run phases (RunActivityAttributes.ContentState.Phase raw values).
const (
	PhaseRunning         = "running"
	PhaseWaitingApproval = "waitingApproval"
	PhaseWaitingInput    = "waitingInput"
	PhaseDone            = "done"
	PhaseFailed          = "failed"
)

// RunContent mirrors RunActivityAttributes.ContentState
// (ios/NexusKit/Sources/NexusCore/Shared/ActivityAttributes.swift).
// Omitted keys decode as nil on iOS; phase, title and costCents are required.
type RunContent struct {
	Phase         string   `json:"phase"`
	Title         string   `json:"title"`
	Detail        string   `json:"detail,omitempty"`
	Progress      *float64 `json:"progress,omitempty"`
	Step          int      `json:"step,omitempty"`
	TotalSteps    int      `json:"totalSteps,omitempty"`
	CostCents     int64    `json:"costCents"`
	ApprovalID    string   `json:"approvalId,omitempty"`
	ApprovalLevel string   `json:"approvalLevel,omitempty"`
	Choices       []string `json:"choices,omitempty"`
}

// RunAttributes mirrors RunActivityAttributes' static attributes.
type RunAttributes struct {
	TaskID      string `json:"taskId"`
	AgentID     string `json:"agentId"`
	AgentName   string `json:"agentName"`
	AgentSymbol string `json:"agentSymbol"`
	NodeName    string `json:"nodeName"`
}

// PulseContent mirrors PulseActivityAttributes.ContentState.
type PulseContent struct {
	Running        int    `json:"running"`
	Waiting        int    `json:"waiting"`
	CostCentsToday int64  `json:"costCentsToday"`
	Headline       string `json:"headline,omitempty"`
	HeadlineSymbol string `json:"headlineSymbol,omitempty"`
}

// PulseAttributes mirrors PulseActivityAttributes' static attributes.
type PulseAttributes struct {
	HubName string `json:"hubName"`
}

type laAlert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Sound string `json:"sound,omitempty"`
}

type laAPS struct {
	Timestamp      int64    `json:"timestamp"`
	Event          string   `json:"event"`
	ContentState   any      `json:"content-state"`
	StaleDate      int64    `json:"stale-date,omitempty"`
	DismissalDate  int64    `json:"dismissal-date,omitempty"`
	RelevanceScore float64  `json:"relevance-score,omitempty"`
	AttributesType string   `json:"attributes-type,omitempty"`
	Attributes     any      `json:"attributes,omitempty"`
	Alert          *laAlert `json:"alert,omitempty"`
}

// AgentInfo is the display data of an agent.
type AgentInfo struct {
	Name   string
	Symbol string // SF Symbol
}

// liveSender delivers a liveactivity push to one token.
type liveSender func(ctx context.Context, t security.LiveActivityToken, payload []byte, prio int, exp time.Time) bool

// LiveManager drives Run and Pulse Live Activities (IOS_LIVE.md §2.5–2.6).
// Handle and Flush are called from the module's event loop.
type LiveManager struct {
	cfg     LiveActivityConfig
	hubName string
	quiet   *QuietHours
	now     func() time.Time
	log     *slog.Logger
	dir     func() Directory
	send    liveSender
	// Optional lookups.
	agentInfo func(ctx context.Context, id string) AgentInfo
	getTask   func(ctx context.Context, id string) *nexusv1.Task

	mu      sync.Mutex
	runs    map[string]*run
	ended   map[string]bool
	noToken map[string]time.Time // negative cache: task has no in-app activity
	tasks   map[string]*nexusv1.Task
	pulse   pulseState
}

type run struct {
	attrs     RunAttributes
	content   RunContent
	sent      []byte // last content-state sent
	lastSent  time.Time
	lastTS    int64
	startedAt time.Time
	devices   []string
	steps     int
}

type pulseState struct {
	live      bool
	startedAt time.Time
	idleSince time.Time
	content   PulseContent
	sent      []byte
	lastSent  time.Time
	lastTS    int64
	costDay   string
}

type laJob struct {
	event        string // start | update | end
	activityType string
	taskID       string
	devices      []string
	since        time.Time
	payload      []byte
	priority     int
	exp          time.Time
}

func (m *LiveManager) init() {
	if m.runs == nil {
		m.runs = map[string]*run{}
		m.ended = map[string]bool{}
		m.noToken = map[string]time.Time{}
		m.tasks = map[string]*nexusv1.Task{}
	}
	if len(m.ended) > maxTracked {
		m.ended = map[string]bool{}
	}
}

func (m *LiveManager) pulseEnabled() bool { return boolOr(m.cfg.Pulse, true) }

// Handle ingests one event.
func (m *LiveManager) Handle(ctx context.Context, ev *nexusv1.Event) {
	var jobs []laJob
	switch b := ev.GetBody().(type) {
	case *nexusv1.Event_Notice:
		if IsFollowMarker(b.Notice) {
			jobs = m.follow(ctx, ev.GetTaskId(), followDevices(b.Notice))
		}
	case *nexusv1.Event_TaskUpdated:
		jobs = m.taskUpdated(ctx, b.TaskUpdated.GetTask())
	case *nexusv1.Event_Approval:
		jobs = m.withRun(ev.GetTaskId(), func(r *run) bool { return m.applyApproval(r, b.Approval) })
	case *nexusv1.Event_Question:
		jobs = m.withRun(ev.GetTaskId(), func(r *run) bool { return m.applyQuestion(r, b.Question) })
	case *nexusv1.Event_ToolCall:
		jobs = m.withRun(ev.GetTaskId(), func(r *run) bool {
			r.steps++
			r.content.Step = r.steps
			if m.cfg.ShowDetails {
				r.content.Detail = truncate(b.ToolCall.GetTool(), 64)
			}
			return false
		})
	case *nexusv1.Event_Cost:
		cents := b.Cost.GetCost().GetAmount().GetMinorUnits()
		m.mu.Lock()
		m.init()
		m.rollDay()
		m.pulse.content.CostCentsToday += cents
		m.mu.Unlock()
		jobs = m.withRun(ev.GetTaskId(), func(r *run) bool {
			r.content.CostCents += cents
			return false
		})
	}
	m.exec(ctx, jobs)
}

func followDevices(n *nexusv1.Notice) []string {
	var out []string
	for _, v := range n.GetData().GetFields()["device_ids"].GetListValue().GetValues() {
		if s := v.GetStringValue(); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (m *LiveManager) rollDay() {
	day := m.now().Format("2006-01-02")
	if m.pulse.costDay != day {
		m.pulse.costDay = day
		m.pulse.content.CostCentsToday = 0
	}
}

// withRun mutates the run of taskID (if tracked). mutate returns true for a
// phase change that must be pushed immediately.
func (m *LiveManager) withRun(taskID string, mutate func(r *run) bool) []laJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	r := m.runs[taskID]
	if r == nil {
		return nil
	}
	prev := r.content.Phase
	urgent := mutate(r)
	if urgent || r.content.Phase != prev {
		return m.sendRun(taskID, r, prev)
	}
	return nil
}

func taskPhase(s nexusv1.TaskState) string {
	switch s {
	case nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL:
		return PhaseWaitingApproval
	case nexusv1.TaskState_TASK_STATE_WAITING_INPUT, nexusv1.TaskState_TASK_STATE_NEEDS_INPUT:
		return PhaseWaitingInput
	case nexusv1.TaskState_TASK_STATE_SUCCEEDED:
		return PhaseDone
	case nexusv1.TaskState_TASK_STATE_FAILED, nexusv1.TaskState_TASK_STATE_CANCELLED:
		return PhaseFailed
	}
	return PhaseRunning
}

// stateLabel matches Nexus_V1_TaskState.label on iOS.
func stateLabel(s nexusv1.TaskState) string {
	switch s {
	case nexusv1.TaskState_TASK_STATE_QUEUED:
		return "Queued"
	case nexusv1.TaskState_TASK_STATE_ROUTING:
		return "Routing"
	case nexusv1.TaskState_TASK_STATE_NEEDS_INPUT:
		return "Needs input"
	case nexusv1.TaskState_TASK_STATE_ASSIGNED:
		return "Assigned"
	case nexusv1.TaskState_TASK_STATE_RUNNING:
		return "Running"
	case nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL:
		return "Waiting for approval"
	case nexusv1.TaskState_TASK_STATE_WAITING_INPUT:
		return "Waiting for you"
	case nexusv1.TaskState_TASK_STATE_ESCALATING:
		return "Trying harder"
	case nexusv1.TaskState_TASK_STATE_SUCCEEDED:
		return "Done"
	case nexusv1.TaskState_TASK_STATE_FAILED:
		return "Failed"
	case nexusv1.TaskState_TASK_STATE_CANCELLED:
		return "Cancelled"
	}
	return "Running"
}

// applyTask updates content from a task snapshot (mirrors
// LiveActivityManager.contentState on iOS, minus sensitive text unless
// show_details).
func (m *LiveManager) applyTask(r *run, t *nexusv1.Task) {
	c := &r.content
	phase := taskPhase(t.State)
	if t.Cost != nil {
		c.CostCents = t.Cost.GetAmount().GetMinorUnits()
	}
	if t.NodeId != "" && r.attrs.NodeName == "" {
		r.attrs.NodeName = t.NodeId
	}
	switch phase {
	case PhaseDone:
		c.Title = "Done"
		if m.cfg.ShowDetails && t.Summary != "" {
			c.Title = t.Summary
		}
		one := 1.0
		c.Progress = &one
		c.ApprovalID, c.ApprovalLevel, c.Choices = "", "", nil
	case PhaseFailed:
		c.Title = stateLabel(t.State)
		if m.cfg.ShowDetails && t.Summary != "" && t.State != nexusv1.TaskState_TASK_STATE_CANCELLED {
			c.Title = t.Summary
		}
		c.ApprovalID, c.ApprovalLevel, c.Choices = "", "", nil
	case PhaseWaitingApproval:
		if c.Phase != PhaseWaitingApproval {
			c.Title = "Waiting for approval"
		}
	case PhaseWaitingInput:
		if c.Phase != PhaseWaitingInput {
			c.Title = "Waiting for you"
		}
	default:
		c.Title = stateLabel(t.State)
		c.ApprovalID, c.ApprovalLevel, c.Choices = "", "", nil
		if !m.cfg.ShowDetails {
			c.Detail = ""
		}
	}
	c.Phase = phase
}

func (m *LiveManager) applyApproval(r *run, a *nexusv1.Approval) bool {
	c := &r.content
	if a.GetState() == nexusv1.ApprovalState_APPROVAL_STATE_PENDING {
		c.Phase = PhaseWaitingApproval
		c.Title = "Waiting for approval"
		c.Detail = ""
		if m.cfg.ShowDetails {
			if a.Title != "" {
				c.Title = a.Title
			}
			c.Detail = a.Summary
		}
		c.ApprovalID = a.Id
		c.ApprovalLevel = "approve"
		if a.Level == nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC {
			c.ApprovalLevel = "biometric"
		}
		return true
	}
	if c.ApprovalID == a.GetId() {
		c.ApprovalID, c.ApprovalLevel = "", ""
		if c.Phase == PhaseWaitingApproval {
			c.Phase = PhaseRunning
			c.Title = "Running"
			c.Detail = ""
		}
	}
	return false
}

func (m *LiveManager) applyQuestion(r *run, q *nexusv1.Question) bool {
	c := &r.content
	c.Phase = PhaseWaitingInput
	c.Title = "Waiting for you"
	c.Detail = ""
	c.Choices = nil
	if m.cfg.ShowDetails {
		c.Detail = q.GetText()
		c.Choices = q.GetChoices()
	}
	return true
}

func (m *LiveManager) autoFollow(t *nexusv1.Task) bool {
	return t.ParentId == "" && t.AgentId != "" && slices.Contains(m.cfg.AutoFollowAgents, t.AgentId)
}

func (m *LiveManager) taskUpdated(ctx context.Context, t *nexusv1.Task) []laJob {
	if t == nil || t.Id == "" {
		return nil
	}
	terminal := TaskTerminal(t.State)
	m.mu.Lock()
	m.init()
	if terminal {
		delete(m.tasks, t.Id)
	} else {
		m.tasks[t.Id] = t
	}
	_, tracked := m.runs[t.Id]
	ended := m.ended[t.Id]
	m.mu.Unlock()

	var jobs []laJob
	if !tracked && !ended {
		switch {
		case !terminal && m.autoFollow(t):
			jobs = append(jobs, m.follow(ctx, t.Id, nil)...)
		case m.hasInAppActivity(ctx, t.Id):
			// Started in the app ("Follow live"): adopt it for remote updates.
			m.mu.Lock()
			if m.runs[t.Id] == nil {
				m.runs[t.Id] = &run{attrs: RunAttributes{TaskID: t.Id, AgentID: t.AgentId}, startedAt: m.now()}
			}
			m.mu.Unlock()
		}
	}
	jobs = append(jobs, m.withRun(t.Id, func(r *run) bool {
		m.applyTask(r, t)
		return false
	})...)
	jobs = append(jobs, m.pulseJobs(false)...)
	return jobs
}

// hasInAppActivity reports whether a device registered an UPDATE token for
// a Run activity of taskID (negative results cached 30 s).
func (m *LiveManager) hasInAppActivity(ctx context.Context, taskID string) bool {
	m.mu.Lock()
	at, cached := m.noToken[taskID]
	now := m.now()
	m.mu.Unlock()
	if cached && now.Sub(at) < 30*time.Second {
		return false
	}
	dir := m.dir()
	if dir == nil {
		return false
	}
	toks, err := dir.LiveActivityTokens(ctx, nexusv1.LiveActivityTokenKind_LIVE_ACTIVITY_TOKEN_KIND_UPDATE, ActivityRun)
	if err == nil {
		for _, t := range toks {
			if t.TaskID == taskID {
				return true
			}
		}
	}
	m.mu.Lock()
	if len(m.noToken) > maxTracked {
		m.noToken = map[string]time.Time{}
	}
	m.noToken[taskID] = now
	m.mu.Unlock()
	return false
}

// follow starts (push-to-start) a Run activity for taskID.
func (m *LiveManager) follow(ctx context.Context, taskID string, devices []string) []laJob {
	if taskID == "" {
		return nil
	}
	m.mu.Lock()
	m.init()
	t := m.tasks[taskID]
	_, exists := m.runs[taskID]
	m.mu.Unlock()
	if exists {
		return nil
	}
	if t == nil && m.getTask != nil {
		t = m.getTask(ctx, taskID)
	}
	if t != nil && TaskTerminal(t.State) {
		return nil
	}
	r := &run{attrs: RunAttributes{TaskID: taskID, AgentName: "Nexus", AgentSymbol: "sparkles"}, devices: devices}
	r.content = RunContent{Phase: PhaseRunning, Title: "Running"}
	if t != nil {
		r.attrs.AgentID = t.AgentId
		r.attrs.NodeName = t.NodeId
		m.applyTask(r, t)
	}
	if r.attrs.AgentID != "" && m.agentInfo != nil {
		if ai := m.agentInfo(ctx, r.attrs.AgentID); ai.Name != "" {
			r.attrs.AgentName = ai.Name
			if ai.Symbol != "" {
				r.attrs.AgentSymbol = ai.Symbol
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runs[taskID] != nil {
		return nil
	}
	now := m.now()
	r.startedAt = now
	m.runs[taskID] = r
	delete(m.ended, taskID)
	content, _ := json.Marshal(r.content)
	r.sent, r.lastSent = content, now
	r.lastTS = now.Unix()
	payload, err := m.payload(laAPS{
		Timestamp:      r.lastTS,
		Event:          "start",
		ContentState:   r.content,
		StaleDate:      now.Add(m.cfg.StaleAfter).Unix(),
		RelevanceScore: relevanceFor(r.content.Phase),
		AttributesType: RunAttributesType,
		Attributes:     r.attrs,
		Alert:          &laAlert{Title: r.attrs.AgentName, Body: "Following live", Sound: ""},
	})
	if err != nil {
		m.log.Warn("notify: live activity start payload", "task", taskID, "err", err)
		return nil
	}
	return []laJob{{event: "start", activityType: ActivityRun, taskID: taskID, devices: devices,
		payload: payload, priority: 10, exp: now.Add(m.cfg.StaleAfter)}}
}

func relevanceFor(phase string) float64 {
	switch phase {
	case PhaseWaitingApproval, PhaseWaitingInput:
		return 100
	case PhaseRunning:
		return 50
	}
	return 25
}

func (m *LiveManager) phaseAlert(r *run) *laAlert {
	title := r.attrs.AgentName
	if title == "" {
		title = "Nexus"
	}
	a := &laAlert{Title: title, Sound: "default"}
	switch r.content.Phase {
	case PhaseWaitingApproval:
		a.Body = "Approval needed"
	case PhaseWaitingInput:
		a.Body = "Waiting for your answer"
	case PhaseDone:
		a.Body = "Done"
	case PhaseFailed:
		a.Body = r.content.Title
	default:
		return nil
	}
	if m.cfg.ShowDetails && r.content.Title != "" {
		a.Body = r.content.Title
	}
	if m.quiet.Active(m.now()) && (r.content.Phase == PhaseDone || r.content.Phase == PhaseFailed) {
		return nil
	}
	return a
}

func (m *LiveManager) nextTS(last *int64, now time.Time) int64 {
	ts := now.Unix()
	if ts <= *last {
		ts = *last + 1
	}
	*last = ts
	return ts
}

// sendRun builds the push for r's current content (m.mu held). A phase
// change (prev != current) goes out at priority 10 with an alert; terminal
// phases end the activity.
func (m *LiveManager) sendRun(taskID string, r *run, prev string) []laJob {
	now := m.now()
	content, _ := json.Marshal(r.content)
	urgent := prev != r.content.Phase || r.content.Phase == PhaseWaitingApproval || r.content.Phase == PhaseWaitingInput
	aps := laAPS{
		Timestamp:      m.nextTS(&r.lastTS, now),
		Event:          "update",
		ContentState:   r.content,
		RelevanceScore: relevanceFor(r.content.Phase),
	}
	prio := 5
	if urgent {
		prio = 10
		aps.Alert = m.phaseAlert(r)
	}
	terminal := r.content.Phase == PhaseDone || r.content.Phase == PhaseFailed
	tooOld := now.Sub(r.startedAt) >= m.cfg.MaxDuration
	if terminal || tooOld {
		aps.Event = "end"
		aps.DismissalDate = now.Add(m.cfg.DismissAfter).Unix()
		if tooOld && !terminal {
			aps.DismissalDate = now.Unix()
		}
		prio = 10
		delete(m.runs, taskID)
		m.ended[taskID] = true
	} else {
		aps.StaleDate = now.Add(m.cfg.StaleAfter).Unix()
	}
	r.sent, r.lastSent = content, now
	payload, err := m.payload(aps)
	if err != nil {
		m.log.Warn("notify: live activity payload", "task", taskID, "err", err)
		return nil
	}
	exp := now.Add(m.cfg.StaleAfter)
	if aps.Event == "end" {
		exp = now.Add(m.cfg.DismissAfter)
	}
	return []laJob{{event: aps.Event, activityType: ActivityRun, taskID: taskID, payload: payload, priority: prio, exp: exp}}
}

// Flush sends coalesced updates whose min interval elapsed, ends
// over-age activities and maintains the Pulse activity. Call it about
// every second.
func (m *LiveManager) Flush(ctx context.Context) {
	m.mu.Lock()
	m.init()
	now := m.now()
	var jobs []laJob
	for id, r := range m.runs {
		if now.Sub(r.startedAt) >= m.cfg.MaxDuration {
			jobs = append(jobs, m.sendRun(id, r, r.content.Phase)...)
			continue
		}
		if now.Sub(r.lastSent) < m.cfg.MinInterval {
			continue
		}
		content, _ := json.Marshal(r.content)
		if string(content) == string(r.sent) {
			continue
		}
		jobs = append(jobs, m.sendRun(id, r, r.content.Phase)...)
	}
	m.mu.Unlock()
	jobs = append(jobs, m.pulseJobs(true)...)
	m.exec(ctx, jobs)
}

// pulseJobs recomputes the Pulse content; starts / updates / ends it.
func (m *LiveManager) pulseJobs(flush bool) []laJob {
	if !m.pulseEnabled() {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	m.rollDay()
	now := m.now()
	p := &m.pulse
	running, waiting := 0, 0
	var head *nexusv1.Task
	for _, t := range m.tasks {
		switch taskPhase(t.State) {
		case PhaseWaitingApproval, PhaseWaitingInput:
			waiting++
			if head == nil || taskPhase(head.State) == PhaseRunning || t.Id > head.Id {
				head = t
			}
		case PhaseRunning:
			if t.ParentId == "" {
				running++
				if head == nil || (taskPhase(head.State) == PhaseRunning && t.Id > head.Id) {
					head = t
				}
			}
		}
	}
	prevWaiting := p.content.Waiting
	p.content.Running, p.content.Waiting = running, waiting
	p.content.Headline, p.content.HeadlineSymbol = "", ""
	if head != nil {
		if m.cfg.ShowDetails {
			p.content.Headline = truncate(taskTitle(head), 64)
		}
		if m.agentInfo != nil && head.AgentId != "" {
			ai := m.agentInfo(context.Background(), head.AgentId)
			if p.content.Headline == "" {
				p.content.Headline = ai.Name
			}
			p.content.HeadlineSymbol = ai.Symbol
		}
	}
	active := running+waiting > 0
	content, _ := json.Marshal(p.content)

	if !p.live {
		if !active {
			return nil
		}
		p.live, p.startedAt, p.idleSince = true, now, time.Time{}
		p.sent, p.lastSent = content, now
		payload, err := m.payload(laAPS{
			Timestamp: m.nextTS(&p.lastTS, now), Event: "start", ContentState: p.content,
			StaleDate: now.Add(m.cfg.StaleAfter).Unix(), RelevanceScore: 10,
			AttributesType: PulseAttributesType, Attributes: PulseAttributes{HubName: m.hubName},
			Alert: &laAlert{Title: m.hubName, Body: "Agents are working"},
		})
		if err != nil {
			return nil
		}
		return []laJob{{event: "start", activityType: ActivityPulse, payload: payload, priority: 10, exp: now.Add(m.cfg.StaleAfter)}}
	}

	if !active {
		if p.idleSince.IsZero() {
			p.idleSince = now
		}
	} else {
		p.idleSince = time.Time{}
	}
	end := (!active && now.Sub(p.idleSince) >= 2*time.Minute) || now.Sub(p.startedAt) >= m.cfg.MaxDuration
	urgent := waiting > prevWaiting
	changed := string(content) != string(p.sent)
	if !end && !(changed && (urgent || (flush && now.Sub(p.lastSent) >= m.cfg.MinInterval))) {
		return nil
	}
	aps := laAPS{Timestamp: m.nextTS(&p.lastTS, now), Event: "update", ContentState: p.content, RelevanceScore: 10}
	prio := 5
	if urgent {
		prio = 10
	}
	job := laJob{event: "update", activityType: ActivityPulse, since: p.startedAt.Add(-time.Minute)}
	if end {
		aps.Event = "end"
		aps.DismissalDate = now.Unix()
		prio = 10
		job.event = "end"
		p.live = false
	} else {
		aps.StaleDate = now.Add(m.cfg.StaleAfter).Unix()
	}
	p.sent, p.lastSent = content, now
	payload, err := m.payload(aps)
	if err != nil {
		return nil
	}
	job.payload, job.priority, job.exp = payload, prio, now.Add(m.cfg.StaleAfter)
	return []laJob{job}
}

// payload marshals aps, shortening strings until it fits MaxPayload.
func (m *LiveManager) payload(aps laAPS) ([]byte, error) {
	for i := range 4 {
		b, err := json.Marshal(map[string]any{"aps": aps})
		if err != nil {
			return nil, err
		}
		if len(b) <= MaxPayload {
			return b, nil
		}
		lim := 256 >> i
		switch c := aps.ContentState.(type) {
		case RunContent:
			c.Title, c.Detail = truncate(c.Title, lim), truncate(c.Detail, lim)
			if len(c.Choices) > 4 {
				c.Choices = c.Choices[:4]
			}
			for j := range c.Choices {
				c.Choices[j] = truncate(c.Choices[j], lim/4)
			}
			aps.ContentState = c
		case PulseContent:
			c.Headline = truncate(c.Headline, lim)
			aps.ContentState = c
		}
		if a, ok := aps.Attributes.(RunAttributes); ok {
			a.AgentName, a.NodeName = truncate(a.AgentName, lim), truncate(a.NodeName, lim)
			aps.Attributes = a
		}
		if aps.Alert != nil {
			al := *aps.Alert
			al.Title, al.Body = truncate(al.Title, lim), truncate(al.Body, lim)
			aps.Alert = &al
		}
	}
	return nil, errors.New("notify: live activity payload exceeds 4 KB")
}

// exec resolves tokens and sends jobs (no lock held).
func (m *LiveManager) exec(ctx context.Context, jobs []laJob) {
	if len(jobs) == 0 {
		return
	}
	dir := m.dir()
	if dir == nil {
		return
	}
	for _, j := range jobs {
		kind := nexusv1.LiveActivityTokenKind_LIVE_ACTIVITY_TOKEN_KIND_UPDATE
		if j.event == "start" {
			kind = nexusv1.LiveActivityTokenKind_LIVE_ACTIVITY_TOKEN_KIND_PUSH_TO_START
		}
		toks, err := dir.LiveActivityTokens(ctx, kind, j.activityType)
		if err != nil {
			m.log.Warn("notify: live activity tokens", "err", err)
			continue
		}
		var skipDevices []string
		if j.event == "start" && j.activityType == ActivityRun {
			// Devices that already show this task (started in-app) get updates only.
			if upd, err := dir.LiveActivityTokens(ctx, nexusv1.LiveActivityTokenKind_LIVE_ACTIVITY_TOKEN_KIND_UPDATE, ActivityRun); err == nil {
				for _, t := range upd {
					if t.TaskID == j.taskID {
						skipDevices = append(skipDevices, t.DeviceID)
					}
				}
			}
		}
		seen := map[string]bool{}
		sent := 0
		for _, t := range toks {
			key := hex.EncodeToString(t.Token)
			if seen[key] || len(t.Token) == 0 {
				continue
			}
			switch {
			case j.event == "start":
				if len(j.devices) > 0 && !slices.Contains(j.devices, t.DeviceID) {
					continue
				}
				if slices.Contains(skipDevices, t.DeviceID) {
					continue
				}
			case j.activityType == ActivityRun:
				if t.TaskID != j.taskID {
					continue
				}
			default: // pulse update/end: tokens of the current activity only
				if t.UpdatedAt.Before(j.since) {
					continue
				}
			}
			seen[key] = true
			if m.send(ctx, t, j.payload, j.priority, j.exp) {
				sent++
			}
		}
		m.log.Debug("notify: live activity push", "event", j.event, "type", j.activityType,
			"task", j.taskID, "priority", j.priority, "tokens", sent)
	}
}
