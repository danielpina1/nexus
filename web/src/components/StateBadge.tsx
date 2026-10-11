import { CheckCircle2, CircleDashed, Clock, Hand, Loader2, MessageCircleQuestion, OctagonX, XCircle, ArrowUpCircle } from "lucide-react";
import { TaskState } from "../gen/nexus/v1/task_pb";
import { taskStateLabel, taskStateTone } from "../lib/format";
import { Badge } from "./ui/Badge";

export function StateIcon({ state, className = "size-3" }: { state: TaskState; className?: string }) {
  switch (state) {
    case TaskState.RUNNING:
    case TaskState.ROUTING:
    case TaskState.ASSIGNED:
      return <Loader2 className={`${className} animate-spin`} />;
    case TaskState.WAITING_APPROVAL:
      return <Hand className={className} />;
    case TaskState.WAITING_INPUT:
    case TaskState.NEEDS_INPUT:
      return <MessageCircleQuestion className={className} />;
    case TaskState.ESCALATING:
      return <ArrowUpCircle className={className} />;
    case TaskState.SUCCEEDED:
      return <CheckCircle2 className={className} />;
    case TaskState.FAILED:
      return <XCircle className={className} />;
    case TaskState.CANCELLED:
      return <OctagonX className={className} />;
    case TaskState.QUEUED:
      return <Clock className={className} />;
    default:
      return <CircleDashed className={className} />;
  }
}

export function StateBadge({ state }: { state: TaskState }) {
  return (
    <Badge tone={taskStateTone(state)}>
      <StateIcon state={state} />
      {taskStateLabel[state]}
    </Badge>
  );
}
