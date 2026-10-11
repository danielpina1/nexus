import { create } from "zustand";
import { ConnectError } from "@connectrpc/connect";

export type ToastTone = "default" | "success" | "error" | "warn";

export interface Toast {
  id: number;
  title: string;
  body?: string;
  tone: ToastTone;
  action?: { label: string; onClick: () => void };
}

interface ToastState {
  toasts: Toast[];
  push: (t: Omit<Toast, "id" | "tone"> & { tone?: ToastTone; duration?: number }) => void;
  remove: (id: number) => void;
}

let nextId = 1;

export const useToasts = create<ToastState>((set, get) => ({
  toasts: [],
  push: ({ duration = 4500, tone = "default", ...t }) => {
    const id = nextId++;
    set({ toasts: [...get().toasts.slice(-4), { ...t, tone, id }] });
    setTimeout(() => get().remove(id), duration);
  },
  remove: (id) => set({ toasts: get().toasts.filter((t) => t.id !== id) }),
}));

export const toast = {
  info: (title: string, body?: string) => useToasts.getState().push({ title, body }),
  success: (title: string, body?: string) => useToasts.getState().push({ title, body, tone: "success" }),
  warn: (title: string, body?: string) => useToasts.getState().push({ title, body, tone: "warn" }),
  error: (title: string, err?: unknown) =>
    useToasts.getState().push({ title, body: err ? errorMessage(err) : undefined, tone: "error", duration: 7000 }),
};

export function errorMessage(err: unknown): string {
  if (err instanceof ConnectError) return err.rawMessage || err.message;
  if (err instanceof Error) return err.message;
  return String(err);
}
