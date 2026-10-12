package triggers

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// DiskHighPercent is the disk usage at which "node.disk_high" fires.
const DiskHighPercent = 90

var bodyOneof = (&nexusv1.Event{}).ProtoReflect().Descriptor().Oneofs().ByName("body")

// EventKind returns the proto field name of the event body ("task_updated",
// "node_status", …).
func EventKind(ev *nexusv1.Event) string {
	fd := ev.ProtoReflect().WhichOneof(bodyOneof)
	if fd == nil {
		return ""
	}
	return string(fd.Name())
}

// Topics returns the trigger topics of an event (stateless):
//
//	node_status  → node.online | node.offline, plus node.disk_high (disk ≥ 90%)
//	task_updated → task.<state> (task.succeeded, task.failed, task.needs_input…)
//	approval     → approval.<state> (approval.pending, approval.approved…)
//	question     → question
//	notice       → notice.<kind> (notice.alert, notice.result…)
//
// Other events (run output, tool calls, costs…) have no topic.
func Topics(ev *nexusv1.Event) []string {
	switch b := ev.GetBody().(type) {
	case *nexusv1.Event_NodeStatus:
		t := []string{"node.offline"}
		if b.NodeStatus.GetOnline() {
			t[0] = "node.online"
		}
		if b.NodeStatus.GetDiskPercent() >= DiskHighPercent {
			t = append(t, "node.disk_high")
		}
		return t
	case *nexusv1.Event_TaskUpdated:
		return []string{"task." + enumSuffix(b.TaskUpdated.GetTask().GetState().String(), "TASK_STATE_")}
	case *nexusv1.Event_Approval:
		return []string{"approval." + enumSuffix(b.Approval.GetState().String(), "APPROVAL_STATE_")}
	case *nexusv1.Event_Question:
		return []string{"question"}
	case *nexusv1.Event_Notice:
		return []string{"notice." + enumSuffix(b.Notice.GetKind().String(), "NOTICE_KIND_")}
	}
	return nil
}

func enumSuffix(s, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(s, prefix))
}

// MatchTopic reports whether topic matches pattern: exact, "*" (anything),
// or a trailing ".*" wildcard ("task.*").
func MatchTopic(pattern, topic string) bool {
	pattern = strings.TrimSpace(pattern)
	switch {
	case pattern == "*" || pattern == topic:
		return true
	case strings.HasSuffix(pattern, ".*"):
		return strings.HasPrefix(topic, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

// EventMap renders an event as JSON-like data (proto field names), e.g.
// {"seq": "12", "task_id": "…", "node_status": {"node_id": "…", "online": false}}.
func EventMap(ev *nexusv1.Event) map[string]any {
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(ev)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

// EventSource is the "event" trigger source: it watches the event bus and
// emits one firing per topic (see Topics) with Key = topic and Payload
// {"topic", "kind", "event"}. State-like topics are de-duplicated: a node
// fires node.offline once per transition, a task fires task.<state> once
// per state, node.disk_high fires when crossing the threshold.
type EventSource struct {
	bus func() (kernel.EventBus, error)
	log *slog.Logger

	mu        sync.Mutex
	nodeOn    map[string]bool
	nodeDisk  map[string]bool
	taskState map[string]string
}

var _ kernel.TriggerSource = (*EventSource)(nil)

// NewEventSource returns the event source; bus is resolved when Run starts.
func NewEventSource(bus func() (kernel.EventBus, error), log *slog.Logger) *EventSource {
	if log == nil {
		log = slog.Default()
	}
	return &EventSource{bus: bus, log: log}
}

func (s *EventSource) Name() string { return SourceEvent }

// Run subscribes to live events (no replay) and emits firings until ctx ends.
// If the bus ends the subscription (slow consumer) it resubscribes.
func (s *EventSource) Run(ctx context.Context, emit func(kernel.Firing)) error {
	bus, err := s.bus()
	if err != nil {
		s.log.Warn("event triggers disabled: no event bus", "err", err)
		<-ctx.Done()
		return nil
	}
	s.reset()
	since, err := bus.LastSeq(ctx)
	if err != nil {
		return err
	}
	for ctx.Err() == nil {
		ch, err := bus.Subscribe(ctx, since, kernel.EventFilter{})
		if err != nil {
			s.log.Warn("event triggers: subscribe", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
			continue
		}
		for ev := range ch {
			if ev.GetSeq() > since {
				since = ev.GetSeq()
			}
			for _, f := range s.Firings(ev) {
				emit(f)
			}
		}
	}
	return nil
}

func (s *EventSource) reset() {
	s.mu.Lock()
	s.nodeOn, s.nodeDisk, s.taskState = map[string]bool{}, map[string]bool{}, map[string]string{}
	s.mu.Unlock()
}

// Firings returns the (de-duplicated) firings for ev.
func (s *EventSource) Firings(ev *nexusv1.Event) []kernel.Firing {
	topics := Topics(ev)
	if len(topics) == 0 {
		return nil
	}
	s.mu.Lock()
	if s.nodeOn == nil {
		s.nodeOn, s.nodeDisk, s.taskState = map[string]bool{}, map[string]bool{}, map[string]string{}
	}
	var keep []string
	for _, t := range topics {
		switch {
		case t == "node.online" || t == "node.offline":
			id := ev.GetNodeStatus().GetNodeId()
			on := t == "node.online"
			if prev, seen := s.nodeOn[id]; seen && prev == on {
				continue
			}
			s.nodeOn[id] = on
		case t == "node.disk_high":
			id := ev.GetNodeStatus().GetNodeId()
			if s.nodeDisk[id] {
				continue
			}
			s.nodeDisk[id] = true
		case strings.HasPrefix(t, "task."):
			id := ev.GetTaskUpdated().GetTask().GetId()
			if s.taskState[id] == t {
				continue
			}
			if len(s.taskState) > 10000 {
				s.taskState = map[string]string{}
			}
			s.taskState[id] = t
		}
		keep = append(keep, t)
	}
	if ns := ev.GetNodeStatus(); ns != nil && ns.GetDiskPercent() < DiskHighPercent {
		delete(s.nodeDisk, ns.GetNodeId())
	}
	s.mu.Unlock()
	if len(keep) == 0 {
		return nil
	}
	m := EventMap(ev)
	kind := EventKind(ev)
	out := make([]kernel.Firing, 0, len(keep))
	for _, t := range keep {
		out = append(out, kernel.Firing{Source: SourceEvent, Key: t,
			Payload: map[string]any{"topic": t, "kind": kind, "event": m},
			Meta:    map[string]string{"seq": strconv.FormatInt(ev.GetSeq(), 10), "kind": kind}})
	}
	return out
}
