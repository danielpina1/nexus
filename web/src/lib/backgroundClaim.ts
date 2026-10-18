// Pure (no imports) so e2e/background-claim.test.ts can run it with node --test.

const BACKGROUND =
  /\b(running|run|working|continu\w*) in the background\b|\b(i'?ll|i will|will) (report|get|come) back\b|\bonce (it|they|the agent|the subagent)s? (finish|finishes|complete|completes|is done|are done)\b|\b(still|is|are) running in parallel\b|\bin a background (task|agent|process)\b/i;

/**
 * Safety net: a final answer that claims work is still going on in the background, while Nexus
 * tracks no running child task, is flagged in the UI.
 */
export function claimsBackgroundWork(text: string): boolean {
  return BACKGROUND.test(text);
}

export function untrackedBackgroundWarning(answer: string, taskActive: boolean, activeChildren: number): boolean {
  return !taskActive && activeChildren === 0 && !!answer && claimsBackgroundWork(answer);
}

export const BACKGROUND_WARNING = "This answer mentions background work that Nexus isn't tracking.";
