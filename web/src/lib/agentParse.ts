import { create } from "@bufbuild/protobuf";
import { AgentSchema, type Agent } from "../gen/nexus/v1/agent_pb";
import { Tier, TrustLevel } from "../gen/nexus/v1/common_pb";
import { asString, asStringList, parseDurationSeconds, splitFrontmatter, type YValue } from "./yaml";

export const RUNTIMES = ["claude-code", "codex", "openai", "ollama", "anthrex", "shell", "browser", "workflow"];
const TIERS: Record<string, Tier> = { simple: Tier.SIMPLE, medium: Tier.MEDIUM, heavy: Tier.HEAVY };
const LEVELS: Record<string, TrustLevel> = {
  auto: TrustLevel.AUTO,
  notify: TrustLevel.NOTIFY,
  approve: TrustLevel.APPROVE,
  biometric: TrustLevel.BIOMETRIC,
};
const ACTION_CLASSES = ["read", "write_internal", "compute", "write_external", "code_push", "spend", "destructive", "device_control"];

const euros = (v: YValue | undefined) =>
  v == null || v === "" ? undefined : { currency: "EUR", minorUnits: BigInt(Math.round(Number(v) * 100)) };

/**
 * Parses an agent note into the Agent message, collecting validation errors instead of throwing.
 * Mirrors the Hub's rules closely enough for previews; the Hub's answer is authoritative.
 */
export function parseAgentMarkdown(markdown: string, path?: string): Agent {
  const errors: string[] = [];
  const { frontmatter: fm, hasFrontmatter, error } = splitFrontmatter(markdown);
  if (!hasFrontmatter) errors.push("Missing YAML frontmatter (the note must start with ---).");
  if (error) errors.push(`Frontmatter: ${error}`);
  if (hasFrontmatter && !error) {
    if (asString(fm.type) !== "agent") errors.push(`type must be "agent" (got "${asString(fm.type) || "—"}").`);
  }
  const id = asString(fm.id);
  if (!id && hasFrontmatter && !error) errors.push("id is required.");
  else if (id && !/^[a-z0-9][a-z0-9-]*$/.test(id)) errors.push("id must be lowercase letters, digits and dashes.");
  const name = asString(fm.name);
  if (!name && hasFrontmatter && !error) errors.push("name is required.");
  const runtime = asString(fm.runtime);
  if (runtime && !RUNTIMES.includes(runtime)) errors.push(`runtime "${runtime}" is not one of ${RUNTIMES.join(", ")}.`);
  if (!runtime && hasFrontmatter && !error) errors.push("runtime is required.");
  const tierName = asString(fm.tier);
  const tier = TIERS[tierName] ?? Tier.UNSPECIFIED;
  if (tierName && tier === Tier.UNSPECIFIED) errors.push(`tier "${tierName}" must be simple, medium or heavy.`);

  const node = (fm.node && typeof fm.node === "object" && !Array.isArray(fm.node) ? fm.node : {}) as Record<string, YValue>;
  const limits = (fm.limits && typeof fm.limits === "object" && !Array.isArray(fm.limits) ? fm.limits : {}) as Record<string, YValue>;
  const policyRaw = (fm.policy && typeof fm.policy === "object" && !Array.isArray(fm.policy) ? fm.policy : {}) as Record<string, YValue>;
  const policy: Record<string, TrustLevel> = {};
  for (const [k, v] of Object.entries(policyRaw)) {
    if (!ACTION_CLASSES.includes(k)) errors.push(`policy: unknown action class "${k}".`);
    const lvl = LEVELS[asString(v)];
    if (!lvl) errors.push(`policy.${k}: "${asString(v)}" must be auto, notify, approve or biometric.`);
    else policy[k] = lvl;
  }

  return create(AgentSchema, {
    id,
    name,
    description: asString(fm.description),
    icon: asString(fm.icon),
    runtime,
    model: asString(fm.model),
    tier,
    nodeTags: asStringList(node.tags),
    tools: asStringList(fm.tools),
    limits: {
      maxRuntimeSeconds: BigInt(parseDurationSeconds(limits.max_runtime)),
      maxCost: euros(limits.max_cost_eur),
      maxSpend: euros(limits.max_spend_eur),
      maxSubtasks: Number(limits.max_subtasks ?? 0) || 0,
    },
    policy,
    note: { id: `agents/${id}`, path: path ?? `agents/${id}.md`, title: name },
    version: Number(fm.version ?? 1) || 1,
    enabled: fm.enabled !== false,
    errors,
  });
}
