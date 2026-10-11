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
}

interface ChatState {
  messages: ChatMessage[];
  activeTaskId?: string;
  setActiveTask: (id: string | undefined) => void;
  send: (input: string, opts?: { agentId?: string }) => Promise<ChatMessage | undefined>;
  clear: () => void;
}

const KEY = "nexus.chat";

interface Stored {
  messages: Array<Omit<ChatMessage, "route" | "sources"> & { route?: JsonValue; sources?: { id: string; path: string; title: string }[] }>;
  activeTaskId?: string;
}

function persist(s: Pick<ChatState, "messages" | "activeTaskId">) {
  if (isMock()) return;
  const stored: Stored = {
    activeTaskId: s.activeTaskId,
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
  setActiveTask: (id) => {
    set({ activeTaskId: id });
    persist(get());
  },
  clear: () => {
    set({ messages: [], activeTaskId: undefined });
    persist(get());
  },
  send: async (input, opts) => {
    const text = input.trim();
    if (!text) return;
    const user: ChatMessage = { id: uid(), role: "user", text, at: Date.now() };
    const reply: ChatMessage = { id: uid(), role: "nexus", text: "", at: Date.now(), pending: true };
    set({ messages: [...get().messages, user, reply] });
    const patch = (p: Partial<ChatMessage>) => {
      set({ messages: get().messages.map((m) => (m.id === reply.id ? { ...m, ...p } : m)) });
      persist(get());
    };
    try {
      const res = await api().command.execute({
        input: text,
        context: { source: "command-bar" },
        route: opts?.agentId ? { agentId: opts.agentId, confidence: 1 } : undefined,
        idempotencyKey: crypto.randomUUID(),
      });
      if (res.task) {
        useEvents.getState().upsertTasks([res.task]);
        set({ activeTaskId: res.task.id });
      }
      const done: Partial<ChatMessage> = {
        pending: false,
        route: res.route,
        taskId: res.task?.id,
        sources: res.sources,
        text: res.question || res.answer || (res.task ? res.task.title || "Working on it" : "Done."),
        choices: res.question ? res.choices : undefined,
        originalInput: res.question ? text : undefined,
      };
      patch(done);
      return { ...reply, ...done };
    } catch (err) {
      patch({ pending: false, error: errorMessage(err), text: "" });
      return undefined;
    }
  },
}));
