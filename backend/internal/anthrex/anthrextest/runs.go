package anthrextest

// Run builds a generic proto::RunInfo map with the fields the Nexus client
// reads; extra entries in opts override or add fields.
func Run(id, state string, tasks []any, opts map[string]any) map[string]any {
	if tasks == nil {
		tasks = []any{}
	}
	r := map[string]any{
		"run_id": id, "goal": "Add password reset", "project": "/repo", "root": "/repo",
		"state": state, "paused_from": nil, "halted_reason": nil, "approved_by": nil,
		"base_branch": "main", "base_sha": "b0b0b0b", "run_branch": "anthrex/" + id + "/integration",
		"run_head": "c1c1c1c", "base_moved": nil, "revision": uint64(1),
		"max_writers": uint8(3), "max_readers": uint8(3), "max_bounces": uint8(2),
		"writers_busy": uint8(0), "readers_busy": uint8(0), "unverified": false, "worker_sandbox": true,
		"trusted_project": []any{}, "rate_limits": map[string]any{}, "tasks": tasks,
		"critical_path": []any{}, "attention": []any{}, "report_path": "/nonexistent/REPORT.md",
		"outcome": nil, "created_at": uint64(1_700_000_000),
	}
	for k, v := range opts {
		r[k] = v
	}
	return r
}

// Task builds a generic proto::TaskInfo map.
func Task(id, title, state, role string, input, output uint64) map[string]any {
	rounds := []any{}
	if role != "" {
		rounds = append(rounds, map[string]any{
			"role": role, "session": uint32(1), "round": uint32(1), "window_id": uint32(5),
			"route": map[string]any{"runtime": "claude", "model": "opus", "effort": "high"},
			"session_id": nil, "started_at": uint64(1_700_000_000), "ended_at": nil, "tool_calls": uint32(4),
			"last_event": uint64(0), "turn_open": false, "turns": uint32(1), "rate_limited": false,
			"open_subagents": uint32(0), "denials": uint32(0),
			"usage": map[string]any{"input": input, "output": output, "cache_read": uint64(0), "cache_write": uint64(0)},
		})
	}
	return map[string]any{
		"id": id, "title": title, "epic": nil, "kind": "code", "size": "M", "hub": false,
		"test_mode": "check", "test_mode_reason": nil, "notes": []any{}, "owns": []any{}, "deps": []any{},
		"implicit_deps": []any{}, "priority": int32(0),
		"route": map[string]any{"runtime": "claude", "model": "opus", "effort": "high"},
		"review_route": nil, "budget": map[string]any{"tool_calls": uint32(100), "minutes": uint32(30), "tokens": nil},
		"spent_session": map[string]any{"tool_calls": uint32(0), "secs": uint64(0), "tokens": uint64(0)},
		"spent_total":   map[string]any{"tool_calls": uint32(4), "secs": uint64(60), "tokens": input + output},
		"state":         state, "block": nil, "rung": uint8(0), "failures": uint8(0),
		"bounces": map[string]any{"done": 0, "proof": 0, "check": 0, "review": 0, "merge": 0},
		"stalls":  uint8(0), "budget_exceeded": uint8(0), "conflicts": uint8(0),
		"branch": "anthrex/" + id, "worktree": "/wt/" + id, "start_commit": nil, "head": nil, "test": nil, "red": nil,
		"done_signal": nil, "rounds": rounds, "reviews": []any{}, "last_check": nil, "last_proof": nil,
		"merge_commit": nil, "merged_without_approval": nil, "salvage_refs": []any{}, "on_critical_path": false, "wave": uint32(1),
		"diff": map[string]any{"files": uint32(2), "hunks": uint32(3), "added": uint32(40), "removed": uint32(5)},
	}
}
