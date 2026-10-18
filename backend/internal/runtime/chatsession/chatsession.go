// Package chatsession keeps chat conversations alive across runs on a node.
//
// A chat message (CommandContext.source "chat") becomes a task whose run
// context carries chat=true, conversation_id and nexus_status (see
// internal/command/chat.go). CLI runtimes use this package to:
//
//   - remember, per conversation and agent, the CLI session id (Claude Code
//     `--resume <id>`) and the directory it was started in (Claude Code keys
//     sessions by cwd, so chat runs use a stable per-agent directory);
//   - keep the last turns (user message + final reply, truncated) so that a
//     fresh session can be primed with them when resume fails or the runtime
//     cannot resume (codex: history in the prompt);
//   - build a compact chat prompt (status + history + message) instead of the
//     generic JSON context dump.
//
// The store is one JSON file per node: <data dir>/chat-sessions.json.
package chatsession

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// Run context keys (set by the command module for chat tasks).
const (
	KeyChat           = "chat"
	KeyConversationID = "conversation_id"
	KeyNexusStatus    = "nexus_status"
)

// Limits of the stored history.
const (
	MaxTurns         = 10
	maxTurnChars     = 2000
	maxConversations = 500
)

// Turn is one exchange.
type Turn struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}

// Entry is the state of one conversation with one agent on one runtime.
type Entry struct {
	SessionID string    `json:"session_id,omitempty"`
	Dir       string    `json:"dir,omitempty"`
	Model     string    `json:"model,omitempty"`
	Turns     []Turn    `json:"turns,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsChat reports whether the assignment is a chat run.
func IsChat(a *nexusv1.RunAssignment) bool {
	return a.GetContext().GetFields()[KeyChat].GetBoolValue()
}

// ConversationID returns the run's conversation id ("" = none).
func ConversationID(a *nexusv1.RunAssignment) string {
	return strings.TrimSpace(a.GetContext().GetFields()[KeyConversationID].GetStringValue())
}

// Key identifies a conversation entry.
func Key(runtime, agentID, conversationID string) string {
	return runtime + "|" + agentID + "|" + conversationID
}

// Store is the node's conversation store (safe for concurrent use).
type Store struct {
	path string
	mu   sync.Mutex
}

// Open returns the store kept in dataDir ("" = in the OS temp dir).
func Open(dataDir string) *Store {
	if dataDir == "" {
		dataDir = os.TempDir()
	}
	return &Store{path: filepath.Join(dataDir, "chat-sessions.json")}
}

func (s *Store) load() map[string]*Entry {
	m := map[string]*Entry{}
	b, err := os.ReadFile(s.path)
	if err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (s *Store) save(m map[string]*Entry) error {
	if len(m) > maxConversations { // drop the oldest
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return m[keys[i]].UpdatedAt.After(m[keys[j]].UpdatedAt) })
		for _, k := range keys[maxConversations:] {
			delete(m, k)
		}
	}
	b, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Get returns a copy of the entry for key (zero Entry when unknown).
func (s *Store) Get(key string) Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.load()[key]; e != nil {
		return *e
	}
	return Entry{}
}

// Record stores the outcome of a turn: the session id and dir (empty keeps
// the current ones; reset=true clears a session that could not be resumed)
// and appends the exchange to the history.
func (s *Store) Record(key, sessionID, dir, model string, turn Turn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.load()
	e := m[key]
	if e == nil {
		e = &Entry{}
		m[key] = e
	}
	if sessionID != "" {
		e.SessionID = sessionID
	}
	if dir != "" {
		e.Dir = dir
	}
	if model != "" {
		e.Model = model
	}
	turn.User, turn.Assistant = clip(turn.User), clip(turn.Assistant)
	if turn.User != "" || turn.Assistant != "" {
		e.Turns = append(e.Turns, turn)
		if len(e.Turns) > MaxTurns {
			e.Turns = e.Turns[len(e.Turns)-MaxTurns:]
		}
	}
	e.UpdatedAt = time.Now().UTC()
	return s.save(m)
}

// Forget clears the session id of key (resume failed); turns are kept.
func (s *Store) Forget(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.load()
	if e := m[key]; e != nil {
		e.SessionID = ""
		return s.save(m)
	}
	return nil
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxTurnChars {
		return string(r[:maxTurnChars]) + "…"
	}
	return s
}

// Prompt builds a chat run's prompt: the live Nexus status, the earlier
// turns (only when the CLI session does not already hold them) and the
// user's message. Other context keys are left out except memory snippets.
func Prompt(a *nexusv1.RunAssignment, history []Turn) string {
	var b strings.Builder
	f := a.GetContext().GetFields()
	if st := strings.TrimSpace(f[KeyNexusStatus].GetStringValue()); st != "" {
		b.WriteString("<nexus_status>\n")
		b.WriteString(st)
		b.WriteString("\n</nexus_status>\n\n")
	}
	if mem := f["memory"]; mem != nil {
		if js, err := json.Marshal(mem.AsInterface()); err == nil && len(js) > 2 {
			b.WriteString("<memory>\n")
			b.Write(js)
			b.WriteString("\n</memory>\n\n")
		}
	}
	if len(history) > 0 {
		b.WriteString("<earlier_conversation>\n")
		for _, t := range history {
			fmt.Fprintf(&b, "User: %s\nYou: %s\n\n", t.User, t.Assistant)
		}
		b.WriteString("</earlier_conversation>\n\n")
	}
	b.WriteString(strings.TrimSpace(a.GetPrompt()))
	for _, at := range a.GetAttachments() {
		name := at.GetName()
		if name == "" {
			name = at.GetId()
		}
		fmt.Fprintf(&b, "\n[attachment: %s (%s) %s]", name, at.GetMimeType(), at.GetUrl())
	}
	return b.String()
}

// Dir returns the stable working directory of an agent's chat sessions
// under dataDir (created on demand).
func Dir(dataDir, runtime, agentID string) (string, error) {
	safe := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, agentID)
	if safe == "" {
		safe = "agent"
	}
	d := filepath.Join(dataDir, "chat", runtime, safe)
	return d, os.MkdirAll(d, 0o700)
}
