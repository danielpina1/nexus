package vault

import (
	"context"
	"fmt"
	"strings"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
	"github.com/danielpina1/nexus/backend/kernel"
)

// EpisodeWriter writes episode notes (what happened in a task) through any
// kernel.Vault, following vault-template/templates/episode.md:
//
//	episodes/2026/10/2026-10-11-buy-the-usual.md
//	---
//	id: <ULID>
//	type: episode
//	title: Buy the usual
//	when: 2026-10-11T21:00:00Z
//	task_id: tsk_…
//	agent: "[[grocery-continente]]"
//	outcome: succeeded
//	cost: 0.42 EUR            (when known)
//	source: ios-share         (when known)
//	---
//	# Buy the usual
//	## Request
//	<prompt>
//	## What happened
//	<summary>
//	## Links
//
// The tasks module can use it as:
//
//	vault.EpisodeWriter{Vault: v}.WriteEpisode(ctx, task, summary)
type EpisodeWriter struct {
	Vault kernel.Vault
	// Now overrides the clock (tests).
	Now func() time.Time
}

// WriteEpisode writes the episode note and returns its reference. The git
// commit carries the task's current run id.
func (w EpisodeWriter) WriteEpisode(ctx context.Context, task *nexusv1.Task, summary string) (*nexusv1.NoteRef, error) {
	if task == nil {
		return nil, fmt.Errorf("%w: nil task", ErrInvalid)
	}
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	rel, md, err := RenderEpisode(task, summary, now)
	if err != nil {
		return nil, err
	}
	// Find a free name (Read fails for missing notes).
	base := strings.TrimSuffix(rel, ".md")
	for i := 2; i < 100; i++ {
		if _, err := w.Vault.Read(ctx, rel); err != nil {
			break
		}
		rel = fmt.Sprintf("%s-%d.md", base, i)
	}
	ctx = WithRunID(ctx, task.GetCurrentRunId())
	n, err := w.Vault.Write(ctx, rel, string(md), "episode: "+episodeTitle(task))
	if err != nil {
		return nil, err
	}
	return n.GetRef(), nil
}

func episodeTitle(task *nexusv1.Task) string {
	if t := strings.TrimSpace(task.GetTitle()); t != "" {
		return t
	}
	if p := strings.TrimSpace(task.GetPrompt()); p != "" {
		return captureTitle(p, "", nil)
	}
	return "Task " + task.GetId()
}

// RenderEpisode builds the path and Markdown of an episode note.
func RenderEpisode(task *nexusv1.Task, summary string, now time.Time) (string, []byte, error) {
	title := episodeTitle(task)
	when := now
	if task.GetUpdatedAt() != nil && task.GetUpdatedAt().IsValid() {
		when = task.GetUpdatedAt().AsTime()
	}
	date := when.Format("2006-01-02")
	rel := fmt.Sprintf("episodes/%s/%s-%s.md", when.Format("2006/01"), date, Slugify(title, 50))

	keys := []string{"id", "type", "title", "when", "task_id", "agent", "outcome"}
	fields := map[string]any{
		"id": NewID(), "type": "episode", "title": title,
		"when": when.UTC().Format(time.RFC3339), "task_id": task.GetId(),
		"agent": "", "outcome": strings.ToLower(strings.TrimPrefix(task.GetState().String(), "TASK_STATE_")),
	}
	if a := task.GetAgentId(); a != "" {
		fields["agent"] = "[[" + a + "]]"
	} else {
		keys = removeKey(keys, "agent")
	}
	if task.GetState() == nexusv1.TaskState_TASK_STATE_UNSPECIFIED {
		keys = removeKey(keys, "outcome")
	}
	if c := task.GetCost().GetAmount(); c != nil && c.GetMinorUnits() != 0 {
		keys = append(keys, "cost")
		fields["cost"] = fmt.Sprintf("%d.%02d %s", c.GetMinorUnits()/100, c.GetMinorUnits()%100, c.GetCurrency())
	}
	if s := task.GetSource(); s != "" {
		keys = append(keys, "source")
		fields["source"] = s
	}
	if p := task.GetParentId(); p != "" {
		keys = append(keys, "parent_task_id")
		fields["parent_task_id"] = p
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = strings.TrimSpace(task.GetSummary())
	}
	body := fmt.Sprintf("# %s\n\n## Request\n%s\n\n## What happened\n%s\n\n## Links\n",
		title, strings.TrimSpace(task.GetPrompt()), summary)
	md, err := note.Render(keys, fields, body)
	return rel, md, err
}

func removeKey(keys []string, k string) []string {
	out := keys[:0:0]
	for _, x := range keys {
		if x != k {
			out = append(out, x)
		}
	}
	return out
}
