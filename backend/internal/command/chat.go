package command

// Chat: CommandContext.source "chat" (the web / app chat) is a conversation
// with an agent, not a one-shot command, so it skips the LLM router (no
// 3–8 s routing call per message):
//
//	"/search <q>"                → SEARCH (vault RAG answer)
//	"/remember <x>", "remember <x>" → CAPTURE
//	"@<agent> <text>"            → TASK for that agent (or team, when the id is a team)
//	anything else                → TASK for command.chat_agent (default "assistant")
//
// Chat tasks carry params the runtimes understand (see
// internal/runtime/chatsession): conversation_id (one CLI session per
// conversation and agent on the node, resumed for follow-ups), chat=true,
// and nexus_status: a compact live snapshot of Nexus (agents, teams, running
// tasks, machines, pending approvals) so the assistant can answer "what can
// you do?" or "what are you doing right now?" from facts.
//
// When command.chat_agent is not in the agent catalog the message falls back
// to the router.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// SourceChat is the CommandContext.source of the chat.
const SourceChat = "chat"

// Chat task params (RunAssignment.context keys).
const (
	ParamConversationID = "conversation_id"
	ParamChat           = "chat"
	ParamNexusStatus    = "nexus_status"
)

// DefaultChatAgent answers chat messages without an @agent.
const DefaultChatAgent = "assistant"

var (
	reMention  = regexp.MustCompile(`^@([\w.-]+)\s+([\s\S]+)$`)
	reSearch   = regexp.MustCompile(`(?i)^/search\s+([\s\S]+)$`)
	reRemember = regexp.MustCompile(`(?i)^(?:/remember|remember)(?:\s+that)?\s+([\s\S]+)$`)
)

func isChat(m *nexusv1.ExecuteRequest) bool { return m.GetContext().GetSource() == SourceChat }

// chatRoute decides a chat message's route without the router. It may
// rewrite m.Input (the command prefix or @mention is stripped). ok=false
// means "use the router" (no chat agent configured).
func (s *Service) chatRoute(ctx context.Context, m *nexusv1.ExecuteRequest) (*nexusv1.Route, bool, error) {
	in := strings.TrimSpace(m.Input)
	if g := reSearch.FindStringSubmatch(in); g != nil {
		m.Input = strings.TrimSpace(g[1])
		return &nexusv1.Route{Kind: kindSearch, Confidence: 1, Reason: "chat: /search"}, true, nil
	}
	if g := reRemember.FindStringSubmatch(in); g != nil {
		m.Input = strings.TrimSpace(g[1])
		return &nexusv1.Route{Kind: kindCapture, Confidence: 1, Reason: "chat: remember"}, true, nil
	}
	if g := reMention.FindStringSubmatch(in); g != nil {
		id := g[1]
		m.Input = strings.TrimSpace(g[2])
		if cat, ok := lookup[kernel.TeamCatalog](s.h, kernel.SvcTeams); ok {
			if _, _, _, err := cat.GetTeam(ctx, id); err == nil {
				return &nexusv1.Route{Kind: kindTask, TeamId: id, Confidence: 1, Reason: "chat: @" + id}, true, nil
			}
		}
		if cat, ok := lookup[kernel.AgentCatalog](s.h, kernel.SvcAgents); ok {
			if _, _, _, err := cat.GetAgent(ctx, id); err != nil {
				return nil, true, connect.NewError(connect.CodeNotFound, fmt.Errorf("no agent or team named %q", id))
			}
		}
		return &nexusv1.Route{Kind: kindTask, AgentId: id, Confidence: 1, Reason: "chat: @" + id}, true, nil
	}
	agent := s.cfg.ChatAgent
	if cat, ok := lookup[kernel.AgentCatalog](s.h, kernel.SvcAgents); ok {
		if _, _, _, err := cat.GetAgent(ctx, agent); err != nil {
			s.log.Warn("chat agent not found; using the router", "agent", agent, "err", err)
			return nil, false, nil
		}
	}
	return &nexusv1.Route{Kind: kindTask, AgentId: agent, Confidence: 1, Reason: "chat"}, true, nil
}

// chatParams adds the chat params (conversation id, live status) to a chat
// task request.
func (s *Service) chatParams(ctx context.Context, m *nexusv1.ExecuteRequest, treq *nexusv1.CreateTaskRequest) {
	fields := map[string]any{ParamChat: true}
	if c := strings.TrimSpace(m.GetContext().GetConversationId()); c != "" {
		fields[ParamConversationID] = c
	}
	if st := s.NexusStatus(ctx); st != "" {
		fields[ParamNexusStatus] = st
	}
	for k, v := range fields {
		if err := setParam(treq, k, v); err != nil {
			s.log.Warn("chat param dropped", "key", k, "err", err)
		}
	}
}

// maxStatusChars keeps the status well under ~2k tokens.
const maxStatusChars = 6000

// NexusStatus renders a compact, live snapshot of Nexus for chat runs:
// agents and teams (id + one line), active root tasks, machines and pending
// approvals. Missing services are skipped.
func (s *Service) NexusStatus(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var b strings.Builder
	fmt.Fprintf(&b, "Nexus status at %s\n", time.Now().UTC().Format("2006-01-02 15:04 UTC"))

	if cat, ok := lookup[kernel.AgentCatalog](s.h, kernel.SvcAgents); ok {
		if list, err := cat.ListAgents(ctx, false); err == nil && len(list) > 0 {
			sort.Slice(list, func(i, j int) bool { return list[i].Id < list[j].Id })
			fmt.Fprintf(&b, "\nAgents (%d; delegate with spawn_agent or tell the user to write @id):\n", len(list))
			for i, a := range list {
				if i == 40 {
					fmt.Fprintf(&b, "- … %d more\n", len(list)-i)
					break
				}
				fmt.Fprintf(&b, "- %s: %s\n", a.Id, oneLine(firstNonEmpty(a.Description, a.Name), 110))
			}
		}
	}
	if cat, ok := lookup[kernel.TeamCatalog](s.h, kernel.SvcTeams); ok {
		if list, err := cat.ListTeams(ctx, false); err == nil && len(list) > 0 {
			sort.Slice(list, func(i, j int) bool { return list[i].Id < list[j].Id })
			fmt.Fprintf(&b, "\nTeams (%d; the user starts one with @id):\n", len(list))
			for i, t := range list {
				if i == 15 {
					fmt.Fprintf(&b, "- … %d more\n", len(list)-i)
					break
				}
				fmt.Fprintf(&b, "- %s: %s\n", t.Id, oneLine(firstNonEmpty(t.Description, t.Name), 110))
			}
		}
	}
	active := []nexusv1.TaskState{
		nexusv1.TaskState_TASK_STATE_QUEUED, nexusv1.TaskState_TASK_STATE_ROUTING, nexusv1.TaskState_TASK_STATE_NEEDS_INPUT,
		nexusv1.TaskState_TASK_STATE_ASSIGNED, nexusv1.TaskState_TASK_STATE_RUNNING, nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL,
		nexusv1.TaskState_TASK_STATE_WAITING_INPUT, nexusv1.TaskState_TASK_STATE_ESCALATING,
	}
	if list, _, err := s.tasks.ListTasks(ctx, &nexusv1.ListTasksRequest{RootsOnly: true, States: active, Page: &nexusv1.PageRequest{PageSize: 10}}); err == nil {
		if len(list) == 0 {
			b.WriteString("\nRunning tasks: none\n")
		} else {
			fmt.Fprintf(&b, "\nRunning tasks (%d shown):\n", len(list))
			for _, t := range list {
				who := firstNonEmpty(t.AgentId, t.TeamId, t.WorkflowId, "?")
				fmt.Fprintf(&b, "- %s [%s] %s: %s\n", t.Id, stateName(t.State), who, oneLine(firstNonEmpty(t.Title, t.Prompt), 90))
			}
		}
	}
	if d, ok := lookup[kernel.Dispatcher](s.h, kernel.SvcDispatcher); ok {
		if nodes, err := d.Nodes(ctx); err == nil {
			if len(nodes) == 0 {
				b.WriteString("\nMachines: none paired\n")
			} else {
				b.WriteString("\nMachines:\n")
				for i, n := range nodes {
					if i == 10 {
						break
					}
					st := "offline"
					if n.GetStatus().GetOnline() {
						st = fmt.Sprintf("online, %d active runs", n.GetStatus().GetActiveRuns())
					}
					var rts []string
					for _, r := range n.GetRuntimes() {
						if r.GetAvailable() {
							rts = append(rts, r.GetName())
						}
					}
					fmt.Fprintf(&b, "- %s (%s; runtimes: %s)\n", firstNonEmpty(n.GetName(), n.GetId()), st, firstNonEmpty(strings.Join(rts, ", "), "none"))
				}
			}
		}
	}
	if ab, ok := lookup[kernel.ApprovalBroker](s.h, kernel.SvcApprovals); ok {
		if list, err := ab.List(ctx, []nexusv1.ApprovalState{nexusv1.ApprovalState_APPROVAL_STATE_PENDING}); err == nil {
			if len(list) == 0 {
				b.WriteString("\nPending approvals: none\n")
			} else {
				fmt.Fprintf(&b, "\nPending approvals (%d):\n", len(list))
				for i, a := range list {
					if i == 5 {
						break
					}
					fmt.Fprintf(&b, "- %s (agent %s, task %s)\n", oneLine(firstNonEmpty(a.Title, a.Summary), 90), a.AgentId, a.TaskId)
				}
			}
		}
	}
	out := b.String()
	if len(out) > maxStatusChars {
		out = out[:maxStatusChars] + "\n…"
	}
	return strings.TrimSpace(out)
}

func stateName(s nexusv1.TaskState) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "TASK_STATE_"))
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func setParam(treq *nexusv1.CreateTaskRequest, k string, v any) error {
	pv, err := structpb.NewValue(v)
	if err != nil {
		return err
	}
	if treq.Params == nil {
		treq.Params = &structpb.Struct{}
	}
	if treq.Params.Fields == nil {
		treq.Params.Fields = map[string]*structpb.Value{}
	}
	treq.Params.Fields[k] = pv
	return nil
}

var errChatEmpty = errors.New("message is empty")

// prepareChat validates and routes a chat message; it returns a copy of m
// with the input rewritten (prefixes stripped).
func (s *Service) prepareChat(ctx context.Context, m *nexusv1.ExecuteRequest) (*nexusv1.ExecuteRequest, *nexusv1.Route, error) {
	m = proto.Clone(m).(*nexusv1.ExecuteRequest)
	r, ok, err := s.chatRoute(ctx, m)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(m.Input) == "" && len(m.Attachments) == 0 {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, errChatEmpty)
	}
	if !ok {
		return m, nil, nil
	}
	return m, r, nil
}
