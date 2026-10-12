package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Run states.
const (
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

// Step states.
const (
	StepPending   = "pending"
	StepRunning   = "running"
	StepWaiting   = "waiting" // approval, question, timer, event, child task
	StepSucceeded = "succeeded"
	StepFailed    = "failed"
	StepSkipped   = "skipped"
	StepDryRun    = "dry_run" // not executed because the run is a dry run
	StepRetry     = "retry"   // marked for re-execution by Retry
	StepCancelled = "cancelled"
)

func runTerminal(s string) bool { return s == RunSucceeded || s == RunFailed || s == RunCancelled }

// stepDone reports whether a persisted step is finished and must not run again.
func stepDone(s string) bool {
	return s == StepSucceeded || s == StepSkipped || s == StepDryRun || s == StepFailed || s == StepCancelled
}

var migrations = []string{
	`CREATE TABLE workflow_runs (
		id              TEXT PRIMARY KEY,
		workflow_id     TEXT NOT NULL,
		parent_run_id   TEXT NOT NULL DEFAULT '',
		depth           INTEGER NOT NULL DEFAULT 0,
		state           TEXT NOT NULL,
		dry_run         INTEGER NOT NULL DEFAULT 0,
		source          TEXT NOT NULL DEFAULT '',
		inputs          TEXT NOT NULL DEFAULT '{}',
		trigger         TEXT NOT NULL DEFAULT '{}',
		definition      TEXT NOT NULL,
		output          TEXT NOT NULL DEFAULT 'null',
		error           TEXT NOT NULL DEFAULT '',
		idempotency_key TEXT,
		created_at      INTEGER NOT NULL,
		updated_at      INTEGER NOT NULL,
		finished_at     INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE UNIQUE INDEX workflow_runs_idem ON workflow_runs(idempotency_key) WHERE idempotency_key IS NOT NULL`,
	`CREATE INDEX workflow_runs_state ON workflow_runs(state)`,
	`CREATE INDEX workflow_runs_wf ON workflow_runs(workflow_id, created_at)`,
	`CREATE TABLE workflow_steps (
		run_id          TEXT NOT NULL,
		path            TEXT NOT NULL,
		step_id         TEXT NOT NULL,
		kind            TEXT NOT NULL,
		state           TEXT NOT NULL,
		output          TEXT NOT NULL DEFAULT 'null',
		result          TEXT NOT NULL DEFAULT 'null',
		error           TEXT NOT NULL DEFAULT '',
		detail          TEXT NOT NULL DEFAULT '',
		task_id         TEXT NOT NULL DEFAULT '',
		approval_id     TEXT NOT NULL DEFAULT '',
		question_id     TEXT NOT NULL DEFAULT '',
		child_run_id    TEXT NOT NULL DEFAULT '',
		answer          TEXT NOT NULL DEFAULT '',
		idempotency_key TEXT NOT NULL DEFAULT '',
		attempt         INTEGER NOT NULL DEFAULT 0,
		started_at      INTEGER NOT NULL DEFAULT 0,
		finished_at     INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (run_id, path)
	)`,
	`CREATE INDEX workflow_steps_question ON workflow_steps(question_id)`,
}

// RunInfo is a workflow run as returned by the service.
type RunInfo struct {
	ID          string         `json:"id"`
	WorkflowID  string         `json:"workflow_id"`
	ParentRunID string         `json:"parent_run_id,omitempty"`
	State       string         `json:"state"`
	DryRun      bool           `json:"dry_run"`
	Source      string         `json:"source,omitempty"`
	Inputs      map[string]any `json:"inputs"`
	Trigger     map[string]any `json:"trigger,omitempty"`
	Output      any            `json:"output"`
	Error       string         `json:"error,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	FinishedAt  time.Time      `json:"finished_at,omitempty"`
	Steps       []StepInfo     `json:"steps,omitempty"`

	depth int
	def   *Workflow
}

// StepInfo is the persisted state of one step execution. Path identifies
// it inside the run: "id", "loop[2]/inner", "branch/inner".
type StepInfo struct {
	Path           string    `json:"path"`
	StepID         string    `json:"step_id"`
	Kind           string    `json:"kind"`
	State          string    `json:"state"`
	Output         any       `json:"output"`
	Result         any       `json:"result"`
	Error          string    `json:"error,omitempty"`
	Detail         string    `json:"detail,omitempty"` // dry-run plan, wait bookkeeping
	TaskID         string    `json:"task_id,omitempty"`
	ApprovalID     string    `json:"approval_id,omitempty"`
	QuestionID     string    `json:"question_id,omitempty"`
	ChildRunID     string    `json:"child_run_id,omitempty"`
	Answer         string    `json:"answer,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	Attempt        int       `json:"attempt"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
}

type store struct {
	db  *sql.DB
	now func() time.Time
}

func ms(t time.Time) int64 { return t.UnixMilli() }
func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func fromJSON(s string) any {
	var v any
	if s == "" || json.Unmarshal([]byte(s), &v) != nil {
		return nil
	}
	return v
}

func (s *store) insertRun(ctx context.Context, r *RunInfo, idemKey string) error {
	now := ms(s.now())
	var key any
	if idemKey != "" {
		key = idemKey
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO workflow_runs (id, workflow_id, parent_run_id, depth, state, dry_run, source, inputs, trigger, definition, idempotency_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.WorkflowID, r.ParentRunID, r.depth, r.State, boolInt(r.DryRun), r.Source,
		mustJSON(r.Inputs), mustJSON(r.Trigger), mustJSON(r.def), key, now, now)
	return err
}

func (s *store) runByIdempotencyKey(ctx context.Context, key string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM workflow_runs WHERE idempotency_key = ?`, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

const runCols = `id, workflow_id, parent_run_id, depth, state, dry_run, source, inputs, trigger, definition, output, error, created_at, updated_at, finished_at`

func scanRun(sc interface{ Scan(...any) error }) (*RunInfo, error) {
	var r RunInfo
	var dry int
	var inputs, trigger, def, output string
	var c, u, f int64
	if err := sc.Scan(&r.ID, &r.WorkflowID, &r.ParentRunID, &r.depth, &r.State, &dry, &r.Source, &inputs, &trigger, &def, &output, &r.Error, &c, &u, &f); err != nil {
		return nil, err
	}
	r.DryRun = dry != 0
	_ = json.Unmarshal([]byte(inputs), &r.Inputs)
	_ = json.Unmarshal([]byte(trigger), &r.Trigger)
	r.Output = fromJSON(output)
	r.CreatedAt, r.UpdatedAt, r.FinishedAt = fromMS(c), fromMS(u), fromMS(f)
	var w Workflow
	if err := json.Unmarshal([]byte(def), &w); err == nil {
		r.def = &w
	}
	if r.Inputs == nil {
		r.Inputs = map[string]any{}
	}
	return &r, nil
}

// ErrNotFound is returned for unknown workflows, runs and questions.
var ErrNotFound = errors.New("not found")

func (s *store) getRun(ctx context.Context, id string) (*RunInfo, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM workflow_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: run %q", ErrNotFound, id)
	}
	return r, err
}

func (s *store) listRuns(ctx context.Context, workflowID, state string, limit int) ([]*RunInfo, error) {
	q := `SELECT ` + runCols + ` FROM workflow_runs`
	var where []string
	var args []any
	if workflowID != "" {
		where = append(where, "workflow_id = ?")
		args = append(args, workflowID)
	}
	if state != "" {
		where = append(where, "state = ?")
		args = append(args, state)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	if limit <= 0 {
		limit = 50
	}
	q += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT %d", limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RunInfo
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *store) finishRun(ctx context.Context, id, state string, output any, errMsg string) error {
	now := ms(s.now())
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_runs SET state = ?, output = ?, error = ?, updated_at = ?, finished_at = ? WHERE id = ?`,
		state, mustJSON(output), errMsg, now, now, id)
	return err
}

func (s *store) setRunState(ctx context.Context, id, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_runs SET state = ?, error = '', updated_at = ?, finished_at = 0 WHERE id = ?`, state, ms(s.now()), id)
	return err
}

const stepCols = `path, step_id, kind, state, output, result, error, detail, task_id, approval_id, question_id, child_run_id, answer, idempotency_key, attempt, started_at, finished_at`

func scanStep(sc interface{ Scan(...any) error }) (*StepInfo, error) {
	var st StepInfo
	var output, result string
	var s, f int64
	if err := sc.Scan(&st.Path, &st.StepID, &st.Kind, &st.State, &output, &result, &st.Error, &st.Detail, &st.TaskID, &st.ApprovalID,
		&st.QuestionID, &st.ChildRunID, &st.Answer, &st.IdempotencyKey, &st.Attempt, &s, &f); err != nil {
		return nil, err
	}
	st.Output, st.Result = fromJSON(output), fromJSON(result)
	st.StartedAt, st.FinishedAt = fromMS(s), fromMS(f)
	return &st, nil
}

func (s *store) getStep(ctx context.Context, runID, path string) (*StepInfo, error) {
	st, err := scanStep(s.db.QueryRowContext(ctx, `SELECT `+stepCols+` FROM workflow_steps WHERE run_id = ? AND path = ?`, runID, path))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return st, err
}

func (s *store) listSteps(ctx context.Context, runID string) ([]StepInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+stepCols+` FROM workflow_steps WHERE run_id = ? ORDER BY started_at, path`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StepInfo
	for rows.Next() {
		st, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *st)
	}
	return out, rows.Err()
}

// putStep upserts the full step row.
func (s *store) putStep(ctx context.Context, runID string, st *StepInfo) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO workflow_steps (run_id, `+stepCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, path) DO UPDATE SET state = excluded.state, output = excluded.output, result = excluded.result,
			error = excluded.error, detail = excluded.detail, task_id = excluded.task_id, approval_id = excluded.approval_id,
			question_id = excluded.question_id, child_run_id = excluded.child_run_id, idempotency_key = excluded.idempotency_key,
			attempt = excluded.attempt, started_at = excluded.started_at, finished_at = excluded.finished_at`,
		runID, st.Path, st.StepID, st.Kind, st.State, mustJSON(st.Output), mustJSON(st.Result), st.Error, st.Detail,
		st.TaskID, st.ApprovalID, st.QuestionID, st.ChildRunID, st.Answer, st.IdempotencyKey, st.Attempt,
		ms(st.StartedAt), ms(st.FinishedAt))
	return err
}

// answer records the answer to a pending question. It returns the run id.
func (s *store) answer(ctx context.Context, questionID, answer string) (string, error) {
	var runID string
	err := s.db.QueryRowContext(ctx, `SELECT run_id FROM workflow_steps WHERE question_id = ? AND state = ?`, questionID, StepWaiting).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: pending workflow question %q", ErrNotFound, questionID)
	}
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE workflow_steps SET answer = ? WHERE question_id = ? AND state = ?`, answer, questionID, StepWaiting)
	return runID, err
}

func (s *store) stepAnswer(ctx context.Context, runID, path string) (string, error) {
	var a string
	err := s.db.QueryRowContext(ctx, `SELECT answer FROM workflow_steps WHERE run_id = ? AND path = ?`, runID, path).Scan(&a)
	return a, err
}

func (s *store) hasQuestion(ctx context.Context, questionID string) bool {
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_steps WHERE question_id = ?`, questionID).Scan(&n)
	return n > 0
}

// markRetry flags failed/cancelled steps of a run for re-execution.
func (s *store) markRetry(ctx context.Context, runID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE workflow_steps SET state = ?, error = '' WHERE run_id = ? AND state IN (?, ?, ?, ?)`,
		StepRetry, runID, StepFailed, StepCancelled, StepRunning, StepWaiting)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
