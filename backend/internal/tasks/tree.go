package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// Task trees (docs/SWARMS.md §6): every sub-task belongs to the tree of its
// root. A tree gets a task_trees row when it becomes a team run or when its
// first sub-task is created; the row holds the effective tree limits, the
// team snapshot, the deadline and the stop reason. Tree limits are enforced
// here, by the hub, never by the model:
//
//   - max_depth / max_tasks when a sub-task is created;
//   - max_runs / max_cost / deadline before every run;
//   - max_parallel and member pools (team member `count`) by holding new
//     sub-tasks queued (phase "waiting_slot") until a slot frees;
//   - max_cost again whenever a cost is recorded: the tree is stopped (root
//     failed, every other task cancelled).

// Phases (Task.phase).
const (
	phaseWaitingSlot = "waiting_slot"
	phaseStrategy    = "strategy"
	phaseReviewing   = "reviewing"
	phaseRevising    = "revising"
	phaseRepairing   = "repairing"
)

// ---------------------------------------------------------------- task ext

// taskExt is engine-internal per-task state, stored as JSON in task_ext.
type taskExt struct {
	// Agent / Instructions define an ephemeral agent (team.spawn, inline
	// orchestrator): AgentJSON is the protojson of *nexusv1.Agent.
	AgentJSON    json.RawMessage `json:"agent,omitempty"`
	Instructions string          `json:"instructions,omitempty"`
	// Repairs counts output-schema repair runs.
	Repairs int `json:"repairs,omitempty"`
	// ReviewRound counts finished critic reviews.
	ReviewRound int `json:"review_round,omitempty"`
	// ReviewOf is set on critic tasks: the task under review.
	ReviewOf string `json:"review_of,omitempty"`
	// OutputSchema is a schema imposed by the hub (critic verdicts, team
	// output) on top of / instead of the agent's.
	OutputSchema map[string]any `json:"output_schema,omitempty"`
	// Reported is the result a team member gave with team.report.
	Reported any `json:"reported,omitempty"`
	// Context is merged into the assignment context (strategy results,
	// review feedback…).
	Context map[string]any `json:"context,omitempty"`
	// Candidate is the result under review (summary + result).
	CandidateSummary string         `json:"candidate_summary,omitempty"`
	CandidateResult  map[string]any `json:"candidate_result,omitempty"`
	// StrategyKey identifies a member task created by a hub-driven strategy
	// step ("drv:pipeline:1"…): a strategy replayed after a hub restart
	// finds it instead of delegating the step again.
	StrategyKey string `json:"strategy_key,omitempty"`
	// Collected lists the sub-tasks whose results team.await already
	// returned to run CollectedRun of this (orchestrator) task. A new run
	// (retry after the hub or node restarted) starts with nothing collected.
	CollectedRun string   `json:"collected_run,omitempty"`
	Collected    []string `json:"collected,omitempty"`
	// Sandbox marks an architect sandbox test run (sandbox.go): strict
	// policy, every gated action is denied and recorded.
	Sandbox        bool            `json:"sandbox,omitempty"`
	SandboxBlocked []sandboxAction `json:"sandbox_blocked,omitempty"`
	SandboxCalls   []string        `json:"sandbox_calls,omitempty"`
	// ChildResumes counts runs resumed after the task's spawn_agent
	// sub-tasks ended (children.go); ChildrenReported lists the sub-tasks
	// whose results were handed to a resumed run.
	ChildResumes     int      `json:"child_resumes,omitempty"`
	ChildrenReported []string `json:"children_reported,omitempty"`
}

func (e *Engine) loadExt(ctx context.Context, taskID string) (*taskExt, error) {
	var data string
	err := e.db.QueryRowContext(ctx, `SELECT data FROM task_ext WHERE task_id = ?`, taskID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return &taskExt{}, nil
	}
	if err != nil {
		return nil, err
	}
	x := &taskExt{}
	return x, json.Unmarshal([]byte(data), x)
}

func (e *Engine) saveExt(ctx context.Context, taskID string, x *taskExt) error {
	b, err := json.Marshal(x)
	if err != nil {
		return err
	}
	_, err = e.db.ExecContext(ctx, `INSERT INTO task_ext (task_id, data) VALUES (?, ?)
		ON CONFLICT(task_id) DO UPDATE SET data = excluded.data`, taskID, string(b))
	return err
}

// updateExt loads, changes and saves a task's ext under extMu.
func (e *Engine) updateExt(ctx context.Context, taskID string, fn func(x *taskExt)) (*taskExt, error) {
	e.extMu.Lock()
	defer e.extMu.Unlock()
	x, err := e.loadExt(ctx, taskID)
	if err != nil {
		return nil, err
	}
	fn(x)
	return x, e.saveExt(ctx, taskID, x)
}

// ---------------------------------------------------------------- tree rows

// treeRec is a task_trees row.
type treeRec struct {
	rootID   string
	teamID   string
	limits   *nexusv1.TeamLimits
	team     *nexusv1.Team // snapshot; nil for plain trees
	mission  string
	stopped  string
	deadline time.Time
	// parentRoot is the root of the tree this one was delegated from
	// (nested team runs); "" for top-level trees.
	parentRoot string
}

func (e *Engine) loadTree(ctx context.Context, rootID string) (*treeRec, error) {
	t := &treeRec{rootID: rootID, limits: &nexusv1.TeamLimits{}}
	var lb, tb []byte
	var deadline int64
	err := e.db.QueryRowContext(ctx, `SELECT team_id, limits, team, mission, stopped, deadline, parent_root FROM task_trees WHERE root_id = ?`, rootID).
		Scan(&t.teamID, &lb, &tb, &t.mission, &t.stopped, &deadline, &t.parentRoot)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(lb) > 0 {
		if err := proto.Unmarshal(lb, t.limits); err != nil {
			return nil, err
		}
	}
	if len(tb) > 0 {
		t.team = &nexusv1.Team{}
		if err := proto.Unmarshal(tb, t.team); err != nil {
			return nil, err
		}
	}
	if deadline > 0 {
		t.deadline = time.UnixMilli(deadline)
	}
	return t, nil
}

func (e *Engine) insertTree(ctx context.Context, t *treeRec) error {
	lb, err := proto.Marshal(t.limits)
	if err != nil {
		return err
	}
	var tb []byte
	if t.team != nil {
		if tb, err = proto.Marshal(t.team); err != nil {
			return err
		}
	}
	var deadline int64
	if !t.deadline.IsZero() {
		deadline = t.deadline.UnixMilli()
	}
	_, err = e.db.ExecContext(ctx, `INSERT OR IGNORE INTO task_trees (root_id, team_id, limits, team, mission, stopped, deadline, created_at, parent_root)
		VALUES (?, ?, ?, ?, ?, '', ?, ?, ?)`, t.rootID, t.teamID, lb, tb, t.mission, deadline, ms(e.now()), t.parentRoot)
	return err
}

// ancestors returns tree and the trees it was delegated from, innermost
// first (nested team runs). Missing rows end the chain.
func (e *Engine) ancestors(ctx context.Context, tree *treeRec) ([]*treeRec, error) {
	var out []*treeRec
	seen := map[string]bool{}
	for t := tree; t != nil && !seen[t.rootID]; {
		seen[t.rootID] = true
		out = append(out, t)
		if t.parentRoot == "" {
			break
		}
		p, err := e.loadTree(ctx, t.parentRoot)
		if err != nil {
			return nil, err
		}
		t = p
	}
	return out, nil
}

// subtreeRoots returns rootID and the roots of every tree nested under it.
func (e *Engine) subtreeRoots(ctx context.Context, rootID string) ([]string, error) {
	out := []string{rootID}
	seen := map[string]bool{rootID: true}
	for i := 0; i < len(out); i++ {
		rows, err := e.db.QueryContext(ctx, `SELECT root_id FROM task_trees WHERE parent_root = ? ORDER BY root_id`, out[i])
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// inList returns "?, ?, …" and the args for an IN clause.
func inList(ids []string) (string, []any) {
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i], args[i] = "?", id
	}
	return strings.Join(ph, ", "), args
}

// subtreeTasks returns every task of the tree and of the trees nested in it.
func (e *Engine) subtreeTasks(ctx context.Context, rootID string) ([]*nexusv1.Task, error) {
	roots, err := e.subtreeRoots(ctx, rootID)
	if err != nil {
		return nil, err
	}
	ph, args := inList(roots)
	return e.queryTasks(ctx, `WHERE root_id IN (`+ph+`) ORDER BY id`, args...)
}

// memberTasks returns the tasks of a tree plus the roots of the trees
// delegated directly from it (sub-team runs are members of their parent).
func (e *Engine) memberTasks(ctx context.Context, rootID string) ([]*nexusv1.Task, error) {
	return e.queryTasks(ctx, `WHERE root_id = ? OR id IN (SELECT root_id FROM task_trees WHERE parent_root = ?) ORDER BY id`, rootID, rootID)
}

// isSubRoot reports whether t is the root of a nested tree (a sub-team
// run delegated from another tree).
func isSubRoot(t *nexusv1.Task) bool { return t.ParentId != "" && t.RootId == t.Id }

// ensureTree returns the tree row of rootID, creating it for a plain tree
// from the root agent's tree limits (AgentLimits max_depth, max_parallel,
// max_tasks, max_tree_cost, max_runs).
func (e *Engine) ensureTree(ctx context.Context, root *taskRec) (*treeRec, error) {
	t, err := e.loadTree(ctx, root.task.Id)
	if err != nil || t != nil {
		return t, err
	}
	t = &treeRec{rootID: root.task.Id, limits: &nexusv1.TeamLimits{}}
	if root.task.AgentId != "" {
		if ra, err := e.resolveAgent(ctx, root.task.Id, root.task.AgentId); err == nil {
			l := ra.agent.GetLimits()
			t.limits = &nexusv1.TeamLimits{MaxParallel: l.GetMaxParallel(), MaxTasks: l.GetMaxTasks(),
				MaxDepth: l.GetMaxDepth(), MaxCost: l.GetMaxTreeCost(), MaxRuns: l.GetMaxRuns()}
		}
	}
	if err := e.insertTree(ctx, t); err != nil {
		return nil, err
	}
	return e.loadTree(ctx, root.task.Id)
}

func (e *Engine) setTreeStopped(ctx context.Context, rootID, reason string) (bool, error) {
	res, err := e.db.ExecContext(ctx, `UPDATE task_trees SET stopped = ? WHERE root_id = ? AND stopped = ''`, reason, rootID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// treeTasks returns the tasks of a tree (root included) in creation order.
func (e *Engine) treeTasks(ctx context.Context, rootID string) ([]*nexusv1.Task, error) {
	return e.queryTasks(ctx, `WHERE root_id = ? ORDER BY id`, rootID)
}

// treeRuns counts the runs of a tree and of the trees nested in it.
func (e *Engine) treeRuns(ctx context.Context, rootID string) (int, error) {
	roots, err := e.subtreeRoots(ctx, rootID)
	if err != nil {
		return 0, err
	}
	ph, args := inList(roots)
	var n int
	err = e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs r JOIN tasks t ON t.id = r.task_id WHERE t.root_id IN (`+ph+`)`, args...).Scan(&n)
	return n, err
}

// treeTaskCount counts the tasks of a tree and of the trees nested in it.
func (e *Engine) treeTaskCount(ctx context.Context, rootID string) (int, error) {
	roots, err := e.subtreeRoots(ctx, rootID)
	if err != nil {
		return 0, err
	}
	ph, args := inList(roots)
	var n int
	err = e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE root_id IN (`+ph+`)`, args...).Scan(&n)
	return n, err
}

// treeCost is the sum of the costs of all tasks of a tree.
func treeCost(tasks []*nexusv1.Task) *nexusv1.Cost {
	var total *nexusv1.Cost
	for _, t := range tasks {
		if t.Cost != nil {
			total = sumCost(total, t.Cost)
		}
	}
	return total
}

func costOver(c *nexusv1.Cost, max *nexusv1.Money) bool {
	if max == nil || max.MinorUnits <= 0 || c.GetAmount() == nil {
		return false
	}
	if max.Currency != "" && c.Amount.Currency != "" && max.Currency != c.Amount.Currency {
		return false
	}
	return c.Amount.MinorUnits >= max.MinorUnits
}

func eur(m *nexusv1.Money) string {
	if m == nil {
		return "€0"
	}
	return fmt.Sprintf("%.2f %s", float64(m.MinorUnits)/100, nonEmpty(m.Currency, "EUR"))
}

// limitErr is the error returned when a tree limit refuses something.
func limitErr(format string, a ...any) error {
	return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf(format, a...))
}

// checkChild enforces the depth / task-count / stop / deadline limits of
// the tree and of every tree it is nested in before a sub-task of parent
// is created. Depths are global (a nested tree's max_depth is stored
// relative to the top-level root).
func (e *Engine) checkChild(ctx context.Context, parent *taskRec, tree *treeRec) error {
	chain, err := e.ancestors(ctx, tree)
	if err != nil {
		return err
	}
	maxDepth := e.cfg.MaxDepth
	for _, t := range chain {
		if t.limits.GetMaxDepth() > 0 && int(t.limits.MaxDepth) < maxDepth {
			maxDepth = int(t.limits.MaxDepth)
		}
	}
	if parent.depth+1 > maxDepth {
		return limitErr("max sub-task depth %d reached", maxDepth)
	}
	for i, t := range chain {
		what := "this run"
		if i > 0 {
			what = "the parent team run " + t.rootID
		}
		if t.stopped != "" {
			return limitErr("the task tree was stopped: %s", t.stopped)
		}
		if !t.deadline.IsZero() && e.now().After(t.deadline) {
			return limitErr("the team run is past its timeout")
		}
		if max := t.limits.GetMaxTasks(); max > 0 {
			n, err := e.treeTaskCount(ctx, t.rootID)
			if err != nil {
				return err
			}
			if n >= int(max) {
				return limitErr("limit of %d tasks for %s reached", max, what)
			}
		}
	}
	return nil
}

// checkRun enforces max_runs / max_cost / deadline / stop of the tree and
// of every tree it is nested in before a new run of a task of the tree. It
// returns the smallest remaining budget (nil = none).
func (e *Engine) checkRun(ctx context.Context, rootID string) (*nexusv1.Money, error) {
	tree, err := e.loadTree(ctx, rootID)
	if err != nil || tree == nil {
		return nil, err
	}
	chain, err := e.ancestors(ctx, tree)
	if err != nil {
		return nil, err
	}
	var left *nexusv1.Money
	for _, t := range chain {
		if t.stopped != "" {
			return nil, limitErr("the task tree was stopped: %s", t.stopped)
		}
		if !t.deadline.IsZero() && e.now().After(t.deadline) {
			return nil, limitErr("the team run is past its timeout")
		}
		if max := t.limits.GetMaxRuns(); max > 0 {
			n, err := e.treeRuns(ctx, t.rootID)
			if err != nil {
				return nil, err
			}
			if n >= int(max) {
				return nil, limitErr("limit of %d runs for this task tree reached", max)
			}
		}
		max := t.limits.GetMaxCost()
		if max == nil || max.MinorUnits <= 0 {
			continue
		}
		tasks, err := e.subtreeTasks(ctx, t.rootID)
		if err != nil {
			return nil, err
		}
		spent := treeCost(tasks)
		if costOver(spent, max) {
			return nil, limitErr("budget of %s for this task tree is used up (%s spent)", eur(max), eur(spent.GetAmount()))
		}
		l := proto.Clone(max).(*nexusv1.Money)
		if spent.GetAmount() != nil {
			l.MinorUnits -= spent.Amount.MinorUnits
		}
		if left == nil || l.MinorUnits < left.MinorUnits {
			left = l
		}
	}
	return left, nil
}

// remainingTime is how long the tree may still run (0 = no deadline): the
// earliest deadline of the tree and the trees it is nested in.
func (e *Engine) remainingTime(ctx context.Context, rootID string) time.Duration {
	tree, err := e.loadTree(ctx, rootID)
	if err != nil || tree == nil {
		return 0
	}
	chain, err := e.ancestors(ctx, tree)
	if err != nil {
		return 0
	}
	var deadline time.Time
	for _, t := range chain {
		if !t.deadline.IsZero() && (deadline.IsZero() || t.deadline.Before(deadline)) {
			deadline = t.deadline
		}
	}
	if deadline.IsZero() {
		return 0
	}
	d := deadline.Sub(e.now())
	if d < time.Second {
		d = time.Second
	}
	return d
}

// ---------------------------------------------------------------- slots

// held reports whether a task waits for a slot.
func held(t *nexusv1.Task) bool { return t.Phase == phaseWaitingSlot && t.State == stQueued }

// active reports whether a task occupies a slot.
// A task waiting for its sub-tasks (phase waiting_children) has no run and
// frees its slot, so its children can start even with max_parallel 1.
func active(t *nexusv1.Task) bool {
	return !IsTerminal(t.State) && !held(t) && t.Phase != phaseStrategy && t.Phase != phaseWaitingChildren
}

// pump starts held tasks of a tree while max_parallel and member pools
// allow, oldest first. A stopped tree cancels its held tasks.
func (e *Engine) pump(ctx context.Context, rootID string) {
	e.slotMu.Lock()
	tree, err := e.loadTree(ctx, rootID)
	if err != nil || tree == nil {
		e.slotMu.Unlock()
		return
	}
	tasks, err := e.memberTasks(ctx, rootID)
	if err != nil {
		e.slotMu.Unlock()
		e.log.Warn("pump: load tree", "root", rootID, "err", err)
		return
	}
	var cancel, start []string
	if tree.stopped != "" {
		for _, t := range tasks {
			if held(t) {
				cancel = append(cancel, t.Id)
			}
		}
	} else {
		pools := map[string]int{}
		if tree.team != nil {
			for _, m := range tree.team.Members {
				pools[m.Role] = int(m.Count)
			}
		}
		running := 0
		perRole := map[string]int{}
		for _, t := range tasks {
			// A sub-team run occupies its member slot until it ends, even
			// while its own strategy runs.
			if t.Id != rootID && (active(t) || (isSubRoot(t) && !IsTerminal(t.State) && !held(t))) {
				running++
				perRole[t.Role]++
			}
		}
		maxPar := int(tree.limits.GetMaxParallel())
		for _, t := range tasks {
			if !held(t) {
				continue
			}
			if maxPar > 0 && running >= maxPar {
				break
			}
			if pool := pools[t.Role]; pool > 0 && perRole[t.Role] >= pool {
				continue
			}
			if _, err := e.mutate(ctx, t.Id, actorHub, func(r *taskRec) error {
				if !held(r.task) {
					return nil
				}
				r.task.Phase = ""
				r.notify = true
				return nil
			}); err != nil {
				e.log.Warn("pump: release task", "task", t.Id, "err", err)
				continue
			}
			running++
			perRole[t.Role]++
			start = append(start, t.Id)
		}
	}
	e.slotMu.Unlock()
	for _, id := range cancel {
		if _, err := e.cancel(ctx, id, "task tree stopped: "+tree.stopped); err != nil {
			e.log.Warn("pump: cancel held task", "task", id, "err", err)
		}
	}
	for _, id := range start {
		if _, err := e.startTask(ctx, id); err != nil {
			e.log.Warn("pump: start task", "task", id, "err", err)
		}
	}
}

// startTask assigns a queued task to its agent (or routes it). A queued
// sub-team root starts its team run.
func (e *Engine) startTask(ctx context.Context, id string) (*nexusv1.Task, error) {
	r, err := e.loadTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.task.State != stQueued {
		return r.task, nil
	}
	if r.task.TeamId != "" && isSubRoot(r.task) {
		tree, err := e.loadTree(ctx, id)
		if err != nil {
			return nil, err
		}
		if tree == nil || tree.team == nil {
			return e.fail(ctx, id, "sub-team run has no team definition")
		}
		return e.launchTeam(ctx, id, tree.team, tree.mission)
	}
	if r.task.AgentId != "" {
		return e.assign(ctx, id, r.task.AgentId, assignOpts{})
	}
	return e.route(ctx, id, r.task.Prompt)
}

// ---------------------------------------------------------------- terminal

// terminalSignal returns a channel closed at the next task end.
func (e *Engine) terminalSignal() <-chan struct{} {
	e.sigMu.Lock()
	defer e.sigMu.Unlock()
	if e.sig == nil {
		e.sig = make(chan struct{})
	}
	return e.sig
}

func (e *Engine) broadcastTerminal() {
	e.sigMu.Lock()
	defer e.sigMu.Unlock()
	if e.sig != nil {
		close(e.sig)
		e.sig = nil
	}
}

// onTerminal runs after a task reached succeeded / failed / cancelled
// (outside the engine lock): it wakes waiters (team.await), finishes reviews,
// frees slots and ends team runs.
func (e *Engine) onTerminal(ctx context.Context, t *nexusv1.Task) {
	e.broadcastTerminal()
	if t.ParentId != "" && t.Source == sourceSpawn {
		// The parent may be waiting for its spawn_agent sub-tasks.
		defer e.childrenSettled(ctx, t.ParentId)
	}
	if x, err := e.loadExt(ctx, t.Id); err == nil && x.ReviewOf != "" {
		e.reviewDone(ctx, t, x)
	}
	tree, err := e.loadTree(ctx, t.RootId)
	if err != nil || tree == nil {
		return
	}
	if t.Id != t.RootId {
		if tree.team != nil {
			e.teamEvent(ctx, tree, nexusv1.TeamEventKind_TEAM_EVENT_KIND_MEMBER_FINISHED, t, truncate(t.Summary, 500),
				map[string]any{"state": stateName(t.State)})
		}
		e.pump(ctx, t.RootId)
		e.refreshTreeCost(ctx, t.RootId)
		return
	}
	// The root ended.
	e.stopTimer(t.Id)
	if tree.parentRoot != "" {
		// A sub-team run is a member of its parent tree.
		if parent, err := e.loadTree(ctx, tree.parentRoot); err == nil && parent != nil {
			if parent.team != nil {
				e.teamEvent(ctx, parent, nexusv1.TeamEventKind_TEAM_EVENT_KIND_MEMBER_FINISHED, t, truncate(t.Summary, 500),
					map[string]any{"state": stateName(t.State), "team": t.TeamId})
			}
			defer func() {
				e.pump(ctx, parent.rootID)
				e.refreshTreeCost(ctx, parent.rootID)
			}()
		}
	}
	if tree.team == nil {
		return
	}
	e.teamEvent(ctx, tree, nexusv1.TeamEventKind_TEAM_EVENT_KIND_FINISHED, t, truncate(t.Summary, 500),
		map[string]any{"state": stateName(t.State)})
	if _, err := e.setTreeStopped(ctx, t.Id, "team run ended ("+stateName(t.State)+")"); err != nil {
		e.log.Warn("mark team run ended", "root", t.Id, "err", err)
	}
	e.cancelDescendants(ctx, t.Id, "team run ended")
}

// cancelDescendants cancels every unfinished task of a tree, and of the
// trees nested in it, except the root.
func (e *Engine) cancelDescendants(ctx context.Context, rootID, reason string) {
	roots, err := e.subtreeRoots(ctx, rootID)
	if err != nil {
		e.log.Warn("cancel descendants", "root", rootID, "err", err)
		return
	}
	ph, args := inList(roots)
	ids, err := e.nonTerminalIDs(ctx, "root_id IN ("+ph+") AND id != ?", append(args, rootID)...)
	if err != nil {
		e.log.Warn("cancel descendants", "root", rootID, "err", err)
		return
	}
	for _, id := range ids {
		if _, err := e.cancel(ctx, id, reason); err != nil {
			e.log.Warn("cancel descendant", "task", id, "err", err)
		}
	}
}

// stopTree stops a whole tree because a limit was hit: no new tasks or
// runs, the root fails with reason and every other task is cancelled.
func (e *Engine) stopTree(ctx context.Context, rootID, reason string) {
	first, err := e.setTreeStopped(ctx, rootID, reason)
	if err != nil {
		e.log.Warn("stop tree", "root", rootID, "err", err)
	}
	if !first {
		return
	}
	e.log.Warn("task tree stopped", "root", rootID, "reason", reason)
	if tree, _ := e.loadTree(ctx, rootID); tree != nil {
		root, _ := e.GetTask(ctx, rootID)
		e.teamEvent(ctx, tree, nexusv1.TeamEventKind_TEAM_EVENT_KIND_LIMIT_REACHED, root, reason, nil)
	}
	e.cancelDescendants(ctx, rootID, reason)
	if _, err := e.fail(ctx, rootID, reason); err != nil {
		e.log.Warn("fail stopped root", "root", rootID, "err", err)
	}
	e.cancelRootRun(ctx, rootID, reason)
}

// cancelRootRun cancels the root's run on its node after the root failed.
func (e *Engine) cancelRootRun(ctx context.Context, rootID, reason string) {
	r, err := e.loadTask(ctx, rootID)
	if err == nil && r.task.CurrentRunId != "" && r.task.NodeId != "" {
		e.cancelOnNode(ctx, r.task.NodeId, r.task.CurrentRunId, reason)
	}
}

// refreshTreeCost updates the root's tree_cost (nested trees included)
// and stops the tree when its budget is used up, then does the same for the
// tree it is nested in.
func (e *Engine) refreshTreeCost(ctx context.Context, rootID string) {
	tree, err := e.loadTree(ctx, rootID)
	if err != nil || tree == nil {
		return
	}
	if tree.parentRoot != "" && tree.parentRoot != rootID {
		defer e.refreshTreeCost(ctx, tree.parentRoot)
	}
	tasks, err := e.subtreeTasks(ctx, rootID)
	if err != nil {
		return
	}
	total := treeCost(tasks)
	if total == nil {
		return
	}
	_, _ = e.mutate(ctx, rootID, actorHub, func(r *taskRec) error {
		if proto.Equal(r.task.TreeCost, total) {
			return nil
		}
		r.task.TreeCost = total
		r.notify = true
		return nil
	})
	if max := tree.limits.GetMaxCost(); tree.stopped == "" && costOver(total, max) {
		e.stopTree(ctx, rootID, fmt.Sprintf("budget exceeded: %s spent of %s", eur(total.GetAmount()), eur(max)))
	}
}

// ---------------------------------------------------------------- deadline

func (e *Engine) startTimer(rootID string, d time.Duration) {
	if d <= 0 {
		return
	}
	e.timerMu.Lock()
	defer e.timerMu.Unlock()
	if e.timers == nil {
		e.timers = map[string]*time.Timer{}
	}
	if old := e.timers[rootID]; old != nil {
		old.Stop()
	}
	e.timers[rootID] = time.AfterFunc(d, func() {
		e.stopTimer(rootID)
		e.stopTree(e.bg, rootID, "team run timed out (deadline reached)")
	})
}

func (e *Engine) stopTimer(rootID string) {
	e.timerMu.Lock()
	defer e.timerMu.Unlock()
	if t := e.timers[rootID]; t != nil {
		t.Stop()
		delete(e.timers, rootID)
	}
}

// ---------------------------------------------------------------- waiting

// waitResult is one task's outcome for team.await.
type waitResult struct {
	TaskID  string         `json:"task_id"`
	Role    string         `json:"role,omitempty"`
	AgentID string         `json:"agent_id,omitempty"`
	State   string         `json:"state"`
	Phase   string         `json:"phase,omitempty"`
	Summary string         `json:"summary,omitempty"`
	Result  map[string]any `json:"result,omitempty"`
}

// maxAwaitSummary caps each summary returned to an orchestrator.
const maxAwaitSummary = 16 << 10

func toWaitResult(t *nexusv1.Task, full bool) waitResult {
	w := waitResult{TaskID: t.Id, Role: t.Role, AgentID: t.AgentId, State: stateName(t.State), Phase: t.Phase}
	if full || IsTerminal(t.State) {
		w.Summary = truncate(t.Summary, maxAwaitSummary)
		if t.Result != nil {
			w.Result = t.Result.AsMap()
		}
	}
	return w
}

// waitTasks blocks until all (or, with any, one) of ids are finished, the
// timeout elapses or ctx ends. It returns the finished and pending tasks.
func (e *Engine) waitTasks(ctx context.Context, ids []string, anyDone bool, timeout time.Duration) (done, pending []*nexusv1.Task, err error) {
	var deadline <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		deadline = t.C
	}
	for {
		sig := e.terminalSignal()
		done, pending = nil, nil
		for _, id := range ids {
			t, err := e.GetTask(ctx, id)
			if err != nil {
				return nil, nil, err
			}
			if IsTerminal(t.State) {
				done = append(done, t)
			} else {
				pending = append(pending, t)
			}
		}
		if len(pending) == 0 || (anyDone && len(done) > 0) {
			return done, pending, nil
		}
		select {
		case <-sig:
		case <-deadline:
			return done, pending, nil
		case <-ctx.Done():
			return done, pending, ctx.Err()
		case <-e.bg.Done():
			return done, pending, e.bg.Err()
		}
	}
}

// ---------------------------------------------------------------- events

// teamEvent publishes a TeamEvent on the root task of a tree.
func (e *Engine) teamEvent(ctx context.Context, tree *treeRec, kind nexusv1.TeamEventKind, subject *nexusv1.Task, text string, data map[string]any) {
	te := &nexusv1.TeamEvent{Kind: kind, TeamId: tree.teamID, RootTaskId: tree.rootID, Text: text}
	if subject != nil {
		te.TaskId, te.Role, te.AgentId = subject.Id, subject.Role, subject.AgentId
	}
	if len(data) > 0 {
		if st, err := structpb.NewStruct(jsonSafeMap(data)); err == nil {
			te.Data = st
		}
	}
	if k, ok := data["key"].(string); ok {
		te.Key = k
	}
	e.publish(ctx, &nexusv1.Event{TaskId: tree.rootID, RootTaskId: tree.rootID, Actor: actorHub,
		Body: &nexusv1.Event_Team{Team: te}})
}

func jsonSafeMap(m map[string]any) map[string]any {
	out, _ := jsonSafe(m).(map[string]any)
	return out
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// sortTasksByID orders tasks by id (creation order).
func sortTasksByID(ts []*nexusv1.Task) {
	sort.Slice(ts, func(i, j int) bool { return ts[i].Id < ts[j].Id })
}
