package learn

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// migrations of the "router.learn" module. They live beside the router's
// own tables (router_decisions, router_corrections) and only reference
// them by decision id.
var migrations = []string{
	`CREATE TABLE router_learn_vectors (
		hash       TEXT PRIMARY KEY,
		model      TEXT NOT NULL,
		dim        INTEGER NOT NULL,
		vec        BLOB NOT NULL,
		created_at INTEGER NOT NULL
	)`,
	// Per-decision stage scores (explainability, offline eval, dashboards).
	`CREATE TABLE router_learn_scores (
		decision_id  TEXT PRIMARY KEY,
		created_at   INTEGER NOT NULL,
		model        TEXT NOT NULL DEFAULT '',
		cluster      INTEGER NOT NULL DEFAULT -1,
		chosen       TEXT NOT NULL DEFAULT '',
		confidence   REAL NOT NULL DEFAULT 0,
		stages       TEXT NOT NULL DEFAULT '{}',
		blended      TEXT NOT NULL DEFAULT '{}',
		used_llm     INTEGER NOT NULL DEFAULT 0,
		runtime_hint TEXT NOT NULL DEFAULT '',
		no_fit       INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE router_learn_outcomes (
		decision_id TEXT PRIMARY KEY,
		task_id     TEXT NOT NULL DEFAULT '',
		created_at  INTEGER NOT NULL,
		state       TEXT NOT NULL DEFAULT '',
		cost_eur    REAL NOT NULL DEFAULT 0,
		latency_ms  INTEGER NOT NULL DEFAULT 0,
		corrected   INTEGER NOT NULL DEFAULT 0,
		denied      INTEGER NOT NULL DEFAULT 0,
		escalated   INTEGER NOT NULL DEFAULT 0,
		runtime     TEXT NOT NULL DEFAULT '',
		reward      REAL NOT NULL DEFAULT -1
	)`,
	`CREATE INDEX router_learn_outcomes_task ON router_learn_outcomes(task_id)`,
	`CREATE TABLE router_learn_state (
		key        TEXT PRIMARY KEY,
		value      BLOB NOT NULL,
		updated_at INTEGER NOT NULL
	)`,
}

// Example is one labelled request for training and evaluation.
type Example struct {
	DecisionID string
	Input      string
	// Label is a candidate id: agent id, "team:<id>" or "workflow:<id>".
	Label    string
	Weight   float64
	Negative bool
	// Note marks examples taken from a note's `examples:` list.
	Note bool
	At   time.Time
}

// Confirmed reports whether the example is a strong label (a correction
// or a successful outcome).
func (e Example) Confirmed() bool { return !e.Negative && !e.Note && e.Weight >= 1 }

// Label weights.
const (
	wCorrection   = 2.0
	wSuccess      = 1.0
	wUnconfirmed  = 0.2
	wWrongAgent   = 0.5 // the agent a correction replaced
	wFailure      = 0.3 // failed / denied / escalated (weak negative)
	wNoteExample  = 0.5
	stateSuccess  = "succeeded"
	stateFailed   = "failed"
	stateCanceled = "cancelled"
)

type historyRow struct {
	id, input, agent, corrected, taskID, chosen, state, runtime string
	created                                                      int64
	kind                                                         int32
	denied, escalated                                            bool
	reward                                                       float64
}

// LoadHistory turns logged decisions joined with their outcomes into
// examples, oldest first. Missing router tables yield no examples.
func LoadHistory(ctx context.Context, db *sql.DB) ([]Example, error) {
	rows, err := db.QueryContext(ctx, `SELECT d.id, d.input, d.agent_id, d.corrected_agent, d.task_id, d.created_at, d.kind,
		COALESCE(s.chosen, ''), COALESCE(o.state, ''), COALESCE(o.denied, 0), COALESCE(o.escalated, 0),
		COALESCE(o.runtime, ''), COALESCE(o.reward, -1)
		FROM router_decisions d
		LEFT JOIN router_learn_scores s ON s.decision_id = d.id
		LEFT JOIN router_learn_outcomes o ON o.decision_id = d.id
		ORDER BY d.created_at, d.id`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, err
	}
	var hist []historyRow
	for rows.Next() {
		var h historyRow
		var denied, esc int
		if err := rows.Scan(&h.id, &h.input, &h.agent, &h.corrected, &h.taskID, &h.created, &h.kind,
			&h.chosen, &h.state, &denied, &esc, &h.runtime, &h.reward); err != nil {
			rows.Close()
			return nil, err
		}
		h.denied, h.escalated = denied != 0, esc != 0
		hist = append(hist, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Example
	for _, h := range hist {
		out = append(out, examplesOf(h)...)
	}
	return out, nil
}

func examplesOf(h historyRow) []Example {
	at := time.UnixMilli(h.created)
	routed := h.chosen
	if routed == "" && h.kind == int32(nexusv1.CommandKind_COMMAND_KIND_TASK) {
		routed = h.agent
	}
	base := Example{DecisionID: h.id, Input: h.input, At: at}
	var out []Example
	switch {
	case strings.TrimSpace(h.input) == "":
		return nil
	case h.corrected != "":
		e := base
		e.Label, e.Weight = h.corrected, wCorrection
		out = append(out, e)
		if routed != "" && routed != h.corrected {
			n := base
			n.Label, n.Weight, n.Negative = routed, wWrongAgent, true
			out = append(out, n)
		}
	case routed == "":
	case h.state == stateSuccess:
		e := base
		e.Label, e.Weight = routed, wSuccess
		out = append(out, e)
	case h.state == stateFailed || h.denied || h.escalated:
		e := base
		e.Label, e.Weight, e.Negative = routed, wFailure, true
		out = append(out, e)
	case h.state == stateCanceled:
	case h.taskID != "":
		e := base
		e.Label, e.Weight = routed, wUnconfirmed
		out = append(out, e)
	}
	return out
}

// ---------------------------------------------------------------- state

func loadState(ctx context.Context, db *sql.DB, key string) ([]byte, error) {
	var b []byte
	err := db.QueryRowContext(ctx, `SELECT value FROM router_learn_state WHERE key = ?`, key).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return b, err
}

func saveState(ctx context.Context, db *sql.DB, key string, v []byte) error {
	_, err := db.ExecContext(ctx, `INSERT INTO router_learn_state (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, v, time.Now().UnixMilli())
	return err
}

// stageScores is what router_learn_scores.stages holds.
type stageScores map[string]map[string]float64

func (l *Learner) saveScores(ctx context.Context, decisionID string, d *Decision) error {
	stages, _ := json.Marshal(d.stages)
	blended, _ := json.Marshal(d.blended)
	_, err := l.db.ExecContext(ctx, `INSERT OR REPLACE INTO router_learn_scores
		(decision_id, created_at, model, cluster, chosen, confidence, stages, blended, used_llm, runtime_hint, no_fit)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		decisionID, l.now().UnixMilli(), d.model, d.cluster, d.chosen, d.confidence, string(stages), string(blended),
		boolInt(d.usedLLM), d.runtimeHint, boolInt(d.noFit))
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
