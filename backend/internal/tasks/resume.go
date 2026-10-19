package tasks

import (
	"context"
	"fmt"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// Restart recovery (docs/SWARMS.md §9). Strategy drivers, deadline timers
// and blocking team.await calls live in hub memory; everything they need is
// in the store, so Resume rebuilds them at start-up for every tree whose
// root has not finished:
//
//   - deadline timers are re-armed with the time left; a deadline that
//     passed while the hub was down stops the tree at once;
//   - a tree stopped mid-way (crash during stopTree) is finished;
//   - roots in phase "strategy" get their driver back. Drivers are
//     idempotent (every step's member task is keyed, see driveStrategy), so
//     finished steps are replayed from their stored results and running
//     ones are awaited, never delegated twice;
//   - queued tasks whose slot was released but that never started are
//     started, and held tasks get a pump (slots may have freed meanwhile);
//   - team.await calls that were blocked simply fail with "the hub is
//     restarting"; the orchestrator calls again and, because results are
//     only marked received when they are returned, gets every result
//     (team.await without task_ids covers finished-but-unreceived ones).

// Resume restores the in-memory parts of unfinished task trees after a
// start. It is safe to call more than once.
func (e *Engine) Resume(ctx context.Context) error {
	rows, err := e.db.QueryContext(ctx, `SELECT tt.root_id FROM task_trees tt JOIN tasks t ON t.id = tt.root_id
		WHERE t.state NOT IN (?, ?, ?) ORDER BY tt.root_id`, int32(stSucceeded), int32(stFailed), int32(stCancelled))
	if err != nil {
		return fmt.Errorf("tasks: resume: %w", err)
	}
	var roots []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		roots = append(roots, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	n := 0
	for _, id := range roots {
		if err := e.resumeTree(ctx, id); err != nil {
			e.log.Warn("tasks: resume task tree", "root", id, "err", err)
			continue
		}
		n++
	}
	if n > 0 {
		e.log.Info("tasks: resumed unfinished task trees", "trees", n)
	}
	return nil
}

func (e *Engine) resumeTree(ctx context.Context, rootID string) error {
	tree, err := e.loadTree(ctx, rootID)
	if err != nil || tree == nil {
		return err
	}
	root, err := e.loadTask(ctx, rootID)
	if err != nil {
		return err
	}
	if IsTerminal(root.task.State) {
		return nil
	}
	// A stop that was interrupted: finish it.
	if tree.stopped != "" {
		e.cancelDescendants(ctx, rootID, tree.stopped)
		if _, err := e.fail(ctx, rootID, tree.stopped); err != nil {
			return err
		}
		e.cancelRootRun(ctx, rootID, tree.stopped)
		return nil
	}
	// A queued sub-team root waits for a slot in its parent tree; its own
	// tree has not started yet (the parent's pump starts it).
	if isSubRoot(root.task) && root.task.State == stQueued && root.task.Phase == phaseWaitingSlot {
		return nil
	}
	if !tree.deadline.IsZero() {
		left := tree.deadline.Sub(e.now())
		if left <= 0 {
			e.stopTree(ctx, rootID, "team run timed out (the deadline passed while the hub was down)")
			return nil
		}
		e.startTimer(rootID, left)
	}
	switch {
	case root.task.State == stQueued && root.task.Phase == phaseStrategy && tree.team != nil:
		switch tree.team.Strategy {
		case nexusv1.TeamStrategy_TEAM_STRATEGY_PARALLEL, nexusv1.TeamStrategy_TEAM_STRATEGY_PIPELINE, nexusv1.TeamStrategy_TEAM_STRATEGY_DEBATE:
			e.startDriver(rootID)
		}
	case root.task.State == stQueued && root.task.Phase == "" && root.task.CurrentRunId == "" && tree.team != nil:
		// Launched but never assigned (crash in between).
		if _, err := e.launchTeam(ctx, rootID, tree.team, tree.mission); err != nil {
			return err
		}
	}
	// Members released from the queue but never started.
	tasks, err := e.treeTasks(ctx, rootID)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.Phase == phaseWaitingChildren {
			// Its sub-tasks may have ended while the hub was down.
			e.childrenSettled(ctx, t.Id)
			continue
		}
		if t.Id != rootID && t.State == stQueued && t.Phase == "" && t.CurrentRunId == "" && t.AgentId != "" {
			if _, err := e.startTask(ctx, t.Id); err != nil {
				e.log.Warn("tasks: resume start task", "task", t.Id, "err", err)
			}
		}
	}
	e.pump(ctx, rootID)
	return nil
}

// resumeDeadlines re-arms (or fires) the deadline timers of unfinished
// trees. Module.Start runs it at once; the rest of Resume waits for nodes.
func (e *Engine) resumeDeadlines(ctx context.Context) {
	rows, err := e.db.QueryContext(ctx, `SELECT tt.root_id, tt.deadline FROM task_trees tt JOIN tasks t ON t.id = tt.root_id
		WHERE tt.deadline > 0 AND tt.stopped = '' AND t.state NOT IN (?, ?, ?)`, int32(stSucceeded), int32(stFailed), int32(stCancelled))
	if err != nil {
		e.log.Warn("tasks: resume deadlines", "err", err)
		return
	}
	type dl struct {
		root string
		at   time.Time
	}
	var list []dl
	for rows.Next() {
		var id string
		var at int64
		if err := rows.Scan(&id, &at); err == nil {
			list = append(list, dl{id, time.UnixMilli(at)})
		}
	}
	rows.Close()
	for _, d := range list {
		if left := d.at.Sub(e.now()); left > 0 {
			e.startTimer(d.root, left)
		} else {
			e.stopTree(ctx, d.root, "team run timed out (the deadline passed while the hub was down)")
		}
	}
}

// waitForNodes waits until a node is connected (runs can be dispatched),
// at most d.
func (e *Engine) waitForNodes(ctx context.Context, d time.Duration) {
	disp, ok := e.dispatcher()
	if !ok || d <= 0 {
		return
	}
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		if nodes, err := disp.Nodes(ctx); err == nil {
			for _, n := range nodes {
				if n.GetStatus().GetOnline() {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-tick.C:
		}
	}
}
