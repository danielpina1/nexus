import { create } from "zustand";
import { toJson, fromJson, type JsonValue } from "@bufbuild/protobuf";
import { RouteSchema, type Route } from "../gen/nexus/v1/command_pb";
import type { NoteRef } from "../gen/nexus/v1/common_pb";
import { api, isMock } from "./api";
import { useEvents } from "./store";
import { errorMessage } from "./toast";
import { readJSON, writeJSON } from "./storage";

export interface ChatMessage {
  id: string;
  role: "user" | "nexus";
  text: string;
  at: number;
  pending?: boolean;
  error?: string;
  route?: Route;
  taskId?: string;
  sources?: NoteRef[];
  choices?: string[];
  /** The input that produced a router question, to re-send with the chosen route. */
  originalInput?: string;
  /** "Create a new agent for this": the architect draft the router started. */
  draftId?: string;
  /** Final answer of the task, kept so old bubbles render without replaying events. */
  finalText?: string;
  /** "Claude Code · claude-sonnet-4-5-20250929" of the run that answered. */
  engine?: string;
  /** Send → first streamed token, measured in the browser. */
  firstTokenMs?: number;
}

interface ChatState {
  messages: ChatMessage[];
  activeTaskId?: string;
  /** Conversation id sent with every message (CommandContext.conversation_id); Clear starts a new one. */
  conversationId: string;
  /** A page the chat wants to open (consumed by the Chat page), e.g. a new architect draft. */
  pendingNav?: string;
  setActiveTask: (id: string | undefined) => void;
  /**
   * source "chat" (the Chat page): a conversation with the assistant, routed by the hub without the
   * LLM router. "command-bar" (default elsewhere): the router decides, "@agent" forces an agent.
   */
  send: (input: string, opts?: { agentId?: string; source?: "chat" | "command-bar" }) => Promise<ChatMessage | undefined>;
  clear: () => void;
  /** Records facts about a reply once known (final text, engine, first-token time). */
  annotate: (id: string, p: Pick<ChatMessage, "finalText" | "engine" | "firstTokenMs">) => void;
}

const KEY = "nexus.chat";

interface Stored {
  messages: Array<Omit<ChatMessage, "route" | "sources"> & { route?: JsonValue; sources?: { id: string; path: string; title: string }[] }>;
  activeTaskId?: string;
  conversationId?: string;
}

const newConversationId = () => (typeof crypto !== "undefined" && "randomUUID" in crypto ? crypto.randomUUID() : Math.random().toString(36).slice(2) + Date.now().toString(36));

function persist(s: Pick<ChatState, "messages" | "activeTaskId" | "conversationId">) {
  if (isMock()) return;
  const stored: Stored = {
    activeTaskId: s.activeTaskId,
    conversationId: s.conversationId,
    messages: s.messages.slice(-200).map((m) => ({
      ...m,
      pending: false,
      route: m.route ? toJson(RouteSchema, m.route) : undefined,
      sources: m.sources?.map((r) => ({ id: r.id, path: r.path, title: r.title })),
    })),
  };
  writeJSON(KEY, stored);
}

export function loadChat() {
  if (isMock()) return;
  const s = readJSON<Stored | null>(KEY, null);
  if (!s) return;
  try {
    useChat.setState({
      activeTaskId: s.activeTaskId,
      conversationId: s.conversationId || newConversationId(),
      messages: s.messages.map((m) => ({
        ...m,
        route: m.route ? fromJson(RouteSchema, m.route) : undefined,
        sources: m.sources as NoteRef[] | undefined,
      })),
    });
  } catch {
    /* ignore corrupt cache */
  }
}

const uid = () => Math.random().toString(36).slice(2, 10);

export const useChat = create<ChatState>((set, get) => ({
  messages: [],
  activeTaskId: undefined,
  conversationId: newConversationId(),
  setActiveTask: (id) => {
    set({ activeTaskId: id });
    persist(get());
  },
  clear: () => {
    set({ messages: [], activeTaskId: undefined, conversationId: newConversationId() });
    persist(get());
  },
  annotate: (id, p) => {
    set({ messages: get().messages.map((m) => (m.id === id ? { ...m, ...p } : m)) });
    persist(get());
  },
  send: async (input, opts) => {
    let text = input.trim();
    if (!text) return;
    const chat = opts?.source !== "command-bar";
    if (!chat) {
      // Command bar: "@agent do something" forces that agent; the router decides otherwise.
      const mention = /^@([\w.-]+)\s+([\s\S]+)$/.exec(text);
      if (mention && !opts?.agentId) {
        opts = { ...opts, agentId: mention[1] };
        text = mention[2].trim();
      }
    }
    // The hub routes chat messages itself (no LLM router): "@agent …", "/search …", "remember …",
    // everything else to the assistant. A forced agent (router question answer) is sent as "@agent".
    const user: ChatMessage = { id: uid(), role: "user", text: opts?.agentId ? `@${opts.agentId} ${text}` : text, at: Date.now() };
    const reply: ChatMessage = { id: uid(), role: "nexus", text: "", at: Date.now(), pending: true };
    set({ messages: [...get().messages, user, reply] });
    const patch = (p: Partial<ChatMessage>) => {
      set({ messages: get().messages.map((m) => (m.id === reply.id ? { ...m, ...p } : m)) });
      persist(get());
    };
    try {
      const res = await api().command.execute({
        input: chat ? user.text : text,
        context: chat ? { source: "chat", conversationId: get().conversationId } : { source: "command-bar" },
        route: !chat && opts?.agentId ? { agentId: opts.agentId, confidence: 1 } : undefined,
        idempotencyKey: crypto.randomUUID(),
      });
      if (res.task) {
        useEvents.getState().upsertTasks([res.task]);
        set({ activeTaskId: res.task.id });
      }
      const draftId = res.route?.params?.architect_draft_id;
      const done: Partial<ChatMessage> = {
        pending: false,
        route: res.route,
        taskId: res.task?.id,
        sources: res.sources,
        text: res.question || res.answer || (res.task ? "" : "Done."),
        choices: res.question ? res.choices : undefined,
        originalInput: res.question ? text : undefined,
        draftId: typeof draftId === "string" && draftId ? draftId : undefined,
      };
      patch(done);
      if (done.draftId) set({ pendingNav: `/architect/${done.draftId}` });
      return { ...reply, ...done };
    } catch (err) {
      patch({ pending: false, error: errorMessage(err), text: "" });
      return undefined;
    }
  },
}));
