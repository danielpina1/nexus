package events

import (
	"slices"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

var bodyOneof = (&nexusv1.Event{}).ProtoReflect().Descriptor().Oneofs().ByName("body")

// Kind returns the proto field name of the event's body ("task_updated",
// "run_output", "approval", …) or "" when the body is unset.
func Kind(ev *nexusv1.Event) string {
	fd := ev.ProtoReflect().WhichOneof(bodyOneof)
	if fd == nil {
		return ""
	}
	return string(fd.Name())
}

// IsInbox reports whether an event belongs to the Inbox stream: approvals
// (pending ones create a card; later states of the same approval are included
// so clients can clear it), questions and notices.
func IsInbox(ev *nexusv1.Event) bool {
	switch ev.GetBody().(type) {
	case *nexusv1.Event_Approval, *nexusv1.Event_Question, *nexusv1.Event_Notice:
		return true
	}
	return false
}

// Match reports whether ev passes filter f.
func Match(f kernel.EventFilter, ev *nexusv1.Event) bool {
	if f.InboxOnly && !IsInbox(ev) {
		return false
	}
	if len(f.TaskIDs) == 0 && len(f.RootTaskIDs) == 0 {
		return true
	}
	if slices.Contains(f.TaskIDs, ev.GetTaskId()) {
		return true
	}
	return ev.GetRootTaskId() != "" && slices.Contains(f.RootTaskIDs, ev.GetRootTaskId()) ||
		ev.GetTaskId() != "" && slices.Contains(f.RootTaskIDs, ev.GetTaskId())
}
