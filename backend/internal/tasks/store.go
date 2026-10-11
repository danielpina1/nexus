package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// migrations for the "tasks" module. The protobuf blobs are the source of
// truth; the other columns are indexed copies for querying.
var migrations = []string{
	`CREATE TABLE tasks (
		id              TEXT PRIMARY KEY,
		parent_id       TEXT NOT NULL DEFAULT '',
		root_id         TEXT NOT NULL,
		depth           INTEGER NOT NULL DEFAULT 0,
		state           INTEGER NOT NULL,
		agent_id        TEXT NOT NULL DEFAULT '',
		node_id         TEXT NOT NULL DEFAULT '',
		current_run_id  TEXT NOT NULL DEFAULT '',
		idempotency_key TEXT,
		question_id     TEXT NOT NULL DEFAULT '',
		approval_id     TEXT NOT NULL DEFAULT '',
		task            BLOB NOT NULL,
		request         BLOB NOT NULL,
		created_at      INTEGER NOT NULL,
		updated_at      INTEGER NOT NULL
	)`,
	`CREATE UNIQUE INDEX tasks_idempotency ON tasks(idempotency_key) WHERE idempotency_key IS NOT NULL`,
	`CREATE INDEX tasks_parent ON tasks(parent_id)`,
	`CREATE INDEX tasks_state ON tasks(state)`,
	`CREATE TABLE runs (
		id               TEXT PRIMARY KEY,
		task_id          TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		attempt          INTEGER NOT NULL,
		agent_id         TEXT NOT NULL,
		node_id          TEXT NOT NULL DEFAULT '',
		tier             INTEGER NOT NULL DEFAULT 0,
		state            TEXT NOT NULL,
		run_token_sha256 BLOB NOT NULL,
		hint             TEXT NOT NULL DEFAULT '',
		output           TEXT NOT NULL DEFAULT '',
		summary          TEXT NOT NULL DEFAULT '',
		cost             BLOB,
		created_at       INTEGER NOT NULL,
		ended_at         INTEGER
	)`,
	`CREATE INDEX runs_task ON runs(task_id)`,
	`CREATE TABLE task_messages (
		idempotency_key TEXT PRIMARY KEY,
		task_id         TEXT NOT NULL,
		created_at      INTEGER NOT NULL
	)`,
}

// Run states stored in runs.state.
const (
	runAssigned   = "assigned"
	runRunning    = "running"
	runSucceeded  = "succeeded"
	runFailed     = "failed"
	runCancelled  = "cancelled"
	runSuperseded = "superseded"
)

// maxRunOutput caps the run output kept for escalation context.
const maxRunOutput = 64 << 10

// taskRec is a task row loaded for mutation.
type taskRec struct {
	task       *nexusv1.Task
	req        *nexusv1.CreateTaskRequest
	depth      int
	questionID string
	approvalID string

	path   []nexusv1.TaskState // state changes applied in this mutation
	notify bool                // publish a TaskUpdated even without a state change
	extra  []*nexusv1.Event    // events published after the TaskUpdated ones
}

// to changes the state, validating it against the transition table.
func (r *taskRec) to(s nexusv1.TaskState) error {
	if r.task.State == s {
		return nil
	}
	if !CanTransition(r.task.State, s) {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("task %s: invalid transition %s → %s", r.task.Id, stateName(r.task.State), stateName(s)))
	}
	r.task.State = s
	r.path = append(r.path, s)
	return nil
}

// runRec is a run row.
type runRec struct {
	id, taskID, agentID, nodeID, state, hint, output, summary string
	attempt                                                   int
	tier                                                      nexusv1.Tier
}

func errNotFound(kind, id string) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s %q not found", kind, id))
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func (e *Engine) insertTask(ctx context.Context, r *taskRec, idemKey string) error {
	tb, err := proto.Marshal(r.task)
	if err != nil {
		return err
	}
	rb, err := proto.Marshal(r.req)
	if err != nil {
		return err
	}
	var key any
	if idemKey != "" {
		key = idemKey
	}
	now := ms(r.task.CreatedAt.AsTime())
	_, err = e.db.ExecContext(ctx, `INSERT INTO tasks
		(id, parent_id, root_id, depth, state, agent_id, node_id, current_run_id, idempotency_key, task, request, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, '', '', ?, ?, ?, ?, ?)`,
		r.task.Id, r.task.ParentId, r.task.RootId, r.depth, int32(r.task.State), r.task.AgentId, key, tb, rb, now, now)
	return err
}

func (e *Engine) saveTask(ctx context.Context, r *taskRec) error {
	tb, err := proto.Marshal(r.task)
	if err != nil {
		return err
	}
	_, err = e.db.ExecContext(ctx, `UPDATE tasks SET state = ?, agent_id = ?, node_id = ?, current_run_id = ?,
		question_id = ?, approval_id = ?, task = ?, updated_at = ? WHERE id = ?`,
		int32(r.task.State), r.task.AgentId, r.task.NodeId, r.task.CurrentRunId,
		r.questionID, r.approvalID, tb, ms(r.task.UpdatedAt.AsTime()), r.task.Id)
	return err
}

const taskCols = `task, request, depth, question_id, approval_id`

func scanTask(sc interface{ Scan(...any) error }) (*taskRec, error) {
	var tb, rb []byte
	r := &taskRec{task: &nexusv1.Task{}, req: &nexusv1.CreateTaskRequest{}}
	if err := sc.Scan(&tb, &rb, &r.depth, &r.questionID, &r.approvalID); err != nil {
		return nil, err
	}
	if err := proto.Unmarshal(tb, r.task); err != nil {
		return nil, err
	}
	if err := proto.Unmarshal(rb, r.req); err != nil {
		return nil, err
	}
	return r, nil
}

func (e *Engine) loadTask(ctx context.Context, id string) (*taskRec, error) {
	r, err := scanTask(e.db.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound("task", id)
	}
	return r, err
}

func (e *Engine) taskByIdempotencyKey(ctx context.Context, key string) (*taskRec, error) {
	r, err := scanTask(e.db.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE idempotency_key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// queryTasks runs a SELECT over task columns and returns the decoded tasks.
func (e *Engine) queryTasks(ctx context.Context, where string, args ...any) ([]*nexusv1.Task, error) {
	rows, err := e.db.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*nexusv1.Task
	for rows.Next() {
		r, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r.task)
	}
	return out, rows.Err()
}

func (e *Engine) insertRun(ctx context.Context, r *runRec, tokenHash []byte) error {
	_, err := e.db.ExecContext(ctx, `INSERT INTO runs
		(id, task_id, attempt, agent_id, node_id, tier, state, run_token_sha256, hint, created_at)
		VALUES (?, ?, ?, ?, '', ?, ?, ?, ?, ?)`,
		r.id, r.taskID, r.attempt, r.agentID, int32(r.tier), runAssigned, tokenHash, r.hint, ms(e.now()))
	return err
}

func (e *Engine) loadRun(ctx context.Context, id string) (*runRec, error) {
	r := &runRec{}
	var tier int32
	err := e.db.QueryRowContext(ctx, `SELECT id, task_id, attempt, agent_id, node_id, tier, state, hint, output, summary
		FROM runs WHERE id = ?`, id).Scan(&r.id, &r.taskID, &r.attempt, &r.agentID, &r.nodeID, &tier, &r.state, &r.hint, &r.output, &r.summary)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound("run", id)
	}
	r.tier = nexusv1.Tier(tier)
	return r, err
}

func (e *Engine) countRuns(ctx context.Context, taskID string) (int, error) {
	var n int
	err := e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE task_id = ?`, taskID).Scan(&n)
	return n, err
}

func (e *Engine) setRunNode(ctx context.Context, runID, nodeID string) error {
	_, err := e.db.ExecContext(ctx, `UPDATE runs SET node_id = ? WHERE id = ?`, nodeID, runID)
	return err
}

// setRunState moves a run to state; terminal run states also set ended_at.
// Runs that already ended are left untouched.
func (e *Engine) setRunState(ctx context.Context, runID, state, summary string) error {
	if runID == "" {
		return nil
	}
	var ended any
	if state != runAssigned && state != runRunning {
		ended = ms(e.now())
	}
	_, err := e.db.ExecContext(ctx, `UPDATE runs SET state = ?, summary = CASE WHEN ? = '' THEN summary ELSE ? END,
		ended_at = COALESCE(ended_at, ?) WHERE id = ? AND state IN (?, ?)`,
		state, summary, summary, ended, runID, runAssigned, runRunning)
	return err
}

func (e *Engine) appendRunOutput(ctx context.Context, runID, text string) error {
	_, err := e.db.ExecContext(ctx, `UPDATE runs SET output = substr(output || ?, -?) WHERE id = ?`, text, maxRunOutput, runID)
	return err
}

func (e *Engine) setRunCost(ctx context.Context, runID string, c *nexusv1.Cost) error {
	b, err := proto.Marshal(c)
	if err != nil {
		return err
	}
	_, err = e.db.ExecContext(ctx, `UPDATE runs SET cost = ? WHERE id = ?`, b, runID)
	return err
}

func (e *Engine) runCost(ctx context.Context, runID string) (*nexusv1.Cost, error) {
	var b []byte
	if err := e.db.QueryRowContext(ctx, `SELECT cost FROM runs WHERE id = ?`, runID).Scan(&b); err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, nil
	}
	c := &nexusv1.Cost{}
	return c, proto.Unmarshal(b, c)
}

func (e *Engine) runTokenHash(ctx context.Context, runID string) ([]byte, error) {
	var b []byte
	err := e.db.QueryRowContext(ctx, `SELECT run_token_sha256 FROM runs WHERE id = ?`, runID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound("run", runID)
	}
	return b, err
}

// recordMessageKey stores a SendMessage idempotency key; it reports false
// when the key was already used.
func (e *Engine) recordMessageKey(ctx context.Context, key, taskID string) (bool, error) {
	res, err := e.db.ExecContext(ctx, `INSERT OR IGNORE INTO task_messages (idempotency_key, task_id, created_at) VALUES (?, ?, ?)`,
		key, taskID, ms(e.now()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// nonTerminalIDs returns ids of tasks that are not finished, oldest first.
func (e *Engine) nonTerminalIDs(ctx context.Context, where string, args ...any) ([]string, error) {
	q := `SELECT id FROM tasks WHERE state NOT IN (?, ?, ?)`
	all := []any{int32(stSucceeded), int32(stFailed), int32(stCancelled)}
	if where != "" {
		q += " AND " + where
		all = append(all, args...)
	}
	rows, err := e.db.QueryContext(ctx, q+` ORDER BY id`, all...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// listWhere builds the WHERE clause for ListTasks.
func listWhere(req *nexusv1.ListTasksRequest, after string) (string, []any) {
	var conds []string
	var args []any
	if len(req.GetStates()) > 0 {
		ph := make([]string, len(req.States))
		for i, s := range req.States {
			ph[i] = "?"
			args = append(args, int32(s))
		}
		conds = append(conds, "state IN ("+strings.Join(ph, ",")+")")
	}
	if req.GetAgentId() != "" {
		conds = append(conds, "agent_id = ?")
		args = append(args, req.AgentId)
	}
	if req.GetNodeId() != "" {
		conds = append(conds, "node_id = ?")
		args = append(args, req.NodeId)
	}
	if req.GetRootsOnly() {
		conds = append(conds, "parent_id = ''")
	}
	if after != "" {
		conds = append(conds, "id < ?")
		args = append(args, after)
	}
	if len(conds) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}
