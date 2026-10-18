import { Link } from "react-router";
import { Cpu, GitBranch } from "lucide-react";
import type { Task } from "../gen/nexus/v1/task_pb";
import { costMinor, cx, formatMoney, timeAgo, tsMs } from "../lib/format";
import { AgentIcon } from "./AgentIcon";
import { StateBadge } from "./StateBadge";
import { agentName, useAgentMap } from "./agentMeta";
import { useNow } from "../lib/useNow";
import { prefetchTask } from "../lib/prefetch";

export function TaskRow({ task, childCount, compact }: { task: Task; childCount?: number; compact?: boolean }) {
  const agents = useAgentMap();
  const now = useNow(30_000);
  const cost = costMinor(task.cost);
  return (
    <Link
      to={`/tasks/${task.id}`}
      onMouseEnter={() => prefetchTask(task.id)}
      onFocus={() => prefetchTask(task.id)}
      className={cx("group flex items-center gap-3 px-4 transition-colors duration-150 hover:bg-surface-2", compact ? "min-h-12 py-2" : "min-h-[60px] py-2.5")}
    >
      <AgentIcon agentId={task.agentId} workflow={!!task.workflowId} size={compact ? "sm" : "md"} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-[13.5px] font-medium text-fg">{task.title || task.prompt}</span>
          {childCount ? (
            <span className="flex items-center gap-0.5 text-[11px] text-muted">
              <GitBranch className="size-3" />
              {childCount}
            </span>
          ) : null}
        </div>
        <div className="mt-0.5 flex items-center gap-1.5 truncate text-xs text-muted">
          <span>{agentName(agents, task.agentId, task.workflowId)}</span>
          {task.nodeId && (
            <>
              <span>·</span>
              <span className="inline-flex items-center gap-0.5">
                <Cpu className="size-3" />
                {task.nodeId}
              </span>
            </>
          )}
          <span>·</span>
          <span>{timeAgo(tsMs(task.createdAt), now)}</span>
        </div>
      </div>
      {cost > 0 && <span className="hidden text-xs text-muted tabular sm:block">{formatMoney(cost, task.cost?.amount?.currency)}</span>}
      <StateBadge state={task.state} />
    </Link>
  );
}
