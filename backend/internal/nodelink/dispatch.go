package nodelink

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"google.golang.org/protobuf/proto"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

var _ kernel.Dispatcher = (*Hub)(nil)

// ErrNoNode is returned by Assign when no connected node matches.
var ErrNoNode = errors.New("nodelink: no connected node matches the selector")

// candidate is the selection view of a node.
type candidate struct {
	id       string
	online   bool
	tags     []string
	runtimes map[string]bool // name -> available
	active   int
}

// TagAny in a selector matches every node.
const TagAny = "any"

// selectNode picks the online node matching sel with the fewest active runs
// (ties broken by id).
func selectNode(cands []candidate, sel kernel.NodeSelector) (string, error) {
	var best *candidate
	for i := range cands {
		c := &cands[i]
		if !c.online {
			continue
		}
		if sel.NodeID != "" && c.id != sel.NodeID {
			continue
		}
		if sel.Runtime != "" && !c.runtimes[sel.Runtime] {
			continue
		}
		if !tagsMatch(sel.Tags, c.tags) {
			continue
		}
		if best == nil || c.active < best.active || (c.active == best.active && c.id < best.id) {
			best = c
		}
	}
	if best == nil {
		return "", ErrNoNode
	}
	return best.id, nil
}

// tagsMatch reports whether want ⊆ have; "any" in want matches everything.
func tagsMatch(want, have []string) bool {
	set := make(map[string]bool, len(have))
	for _, t := range have {
		set[t] = true
	}
	for _, t := range want {
		if t == "" || t == TagAny {
			continue
		}
		if !set[t] {
			return false
		}
	}
	return true
}

func (hb *Hub) candidatesLocked() []candidate {
	out := make([]candidate, 0, len(hb.nodes))
	for _, ns := range hb.nodes {
		c := candidate{id: ns.id, online: ns.online, active: ns.active, runtimes: map[string]bool{}}
		if ns.node != nil {
			c.tags = ns.node.Tags
			for _, rt := range ns.node.Runtimes {
				if rt.Available {
					c.runtimes[rt.Name] = true
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// Assign implements kernel.Dispatcher. When sel.Runtime is empty the agent's
// runtime is required.
func (hb *Hub) Assign(ctx context.Context, sel kernel.NodeSelector, a *nexusv1.RunAssignment) (string, error) {
	if a == nil || a.RunId == "" {
		return "", errors.New("nodelink: assignment without run id")
	}
	if sel.Runtime == "" {
		sel.Runtime = a.GetAgent().GetRuntime()
	}
	if len(sel.Tags) == 0 {
		sel.Tags = a.GetAgent().GetNodeTags()
	}
	hb.mu.Lock()
	if ri, dup := hb.runs[a.RunId]; dup {
		hb.mu.Unlock()
		return ri.nodeID, fmt.Errorf("nodelink: run %s already assigned", a.RunId)
	}
	id, err := selectNode(hb.candidatesLocked(), sel)
	if err != nil {
		hb.mu.Unlock()
		return "", fmt.Errorf("%w (node=%q tags=%v runtime=%q)", err, sel.NodeID, sel.Tags, sel.Runtime)
	}
	ns := hb.nodes[id]
	hb.runs[a.RunId] = &runInfo{nodeID: id, taskID: a.TaskId, token: a.RunToken, agent: a.Agent}
	ns.active++
	hb.mu.Unlock()

	msg := &nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Assign{Assign: proto.Clone(a).(*nexusv1.RunAssignment)}}
	if err := hb.send(ns, msg); err != nil {
		hb.mu.Lock()
		delete(hb.runs, a.RunId)
		ns.active--
		hb.mu.Unlock()
		return "", err
	}
	return id, nil
}

func (hb *Hub) node(id string) (*nodeState, error) {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	ns, ok := hb.nodes[id]
	if !ok {
		return nil, fmt.Errorf("nodelink: unknown node %q", id)
	}
	return ns, nil
}

// SendInput implements kernel.Dispatcher. Queued while the node is offline.
func (hb *Hub) SendInput(ctx context.Context, nodeID string, in *nexusv1.RunInput) error {
	if in.GetRunId() == "" {
		return errors.New("nodelink: input without run id")
	}
	ns, err := hb.node(nodeID)
	if err != nil {
		return err
	}
	return hb.send(ns, &nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Input{Input: in}})
}

// Resolve implements kernel.Dispatcher.
func (hb *Hub) Resolve(ctx context.Context, nodeID string, r *nexusv1.ApprovalResolution) error {
	ns, err := hb.node(nodeID)
	if err != nil {
		return err
	}
	return hb.send(ns, &nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Resolution{Resolution: r}})
}

// Cancel implements kernel.Dispatcher.
func (hb *Hub) Cancel(ctx context.Context, nodeID string, c *nexusv1.RunCancel) error {
	ns, err := hb.node(nodeID)
	if err != nil {
		return err
	}
	return hb.send(ns, &nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Cancel{Cancel: c}})
}

// Nodes implements kernel.Dispatcher.
func (hb *Hub) Nodes(ctx context.Context) ([]*nexusv1.Node, error) {
	ns := hb.snapshot()
	hb.mu.Lock()
	for _, n := range ns {
		if s := hb.nodes[n.Id]; s != nil {
			n.Status.ActiveRuns = int32(s.active)
		}
	}
	hb.mu.Unlock()
	sort.Slice(ns, func(i, j int) bool { return ns[i].Id < ns[j].Id })
	return ns, nil
}
