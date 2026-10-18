import { prefetchPath } from "../app/registry";
import { queryClient } from "./queryClient";
import { api } from "./api";

/** Hover/focus intent: warm the task page chunk and its data. */
export function prefetchTask(id: string) {
  prefetchPath(`/tasks/${id}`);
  void queryClient.prefetchQuery({ queryKey: ["task", id], queryFn: () => api().tasks.getTask({ id, includeChildren: true }), staleTime: 30_000 });
}
