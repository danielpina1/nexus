# Routing: the learning router

> Status: implemented (backend/internal/router, backend/internal/router/learn). See also
> [AGENTS.md §4](AGENTS.md#4-router).

The router answers *what is this input, and who should handle it?* It routes to **agents**,
**teams** (`team:<id>`) and **workflows** (`workflow:<id>` → a WORKFLOW route). The learning stage
(module `router.learn`, enabled by default) makes it improve from the owner's own decisions:
corrections, task outcomes and the agents' `examples:`.

```
input ─► 1 rules (regex, URL domains, share types, capture phrases) ──strong──► route (+ runtime hint)
           │ not strong
           ▼
         2 learning stage
           A retrieval: embeddings of candidate descriptions + examples, kNN over past requests
           B classifier: online multinomial logistic regression (embedding + rule features)
           ·  rules: keyword overlap
           → blend → calibrated confidence
           │ confidence < accept, or classifier and retrieval disagree
           ▼
         3 LLM classifier (fast-local → smart), blended in as one more stage
           ▼
         C bandit: runtime for the chosen agent (Thompson sampling per request cluster)
           ▼
         route {agent|team|workflow, confidence, reason, alternatives,
                params: {decision_id, runtime_hint?, no_fit?}}
```

Without the `router.learn` module the router behaves as before (rules → LLM).

## Candidates

| Kind | Source | Text embedded |
|---|---|---|
| agent | `agents` catalog (enabled agents) | `name: description` + each `examples:` entry |
| team | `teams` catalog (`team:<id>`) | description + trigger phrases |
| workflow | `workflows` service (`workflow:<id>`) | description + `intent:` phrases |

`examples:` is an optional list in agent frontmatter (read from the raw note by the learner; agentdef
ignores it):

```yaml
examples:
  - "we're out of milk and coffee"
  - "order the weekly supermarket run"
```

Missing services are skipped (no teams module → no team candidates).

## Stages

**A · Retrieval.** Every text is embedded with `kernel.LLM.Embed` and the alias `embed` (local
Ollama, e.g. `nomic-embed-text`). Vectors are cached in `router_learn_vectors` keyed by
`sha256(model, text)`. Two signals:
- *description*: max cosine between the request and each candidate's texts → softmax (T = 10);
- *kNN*: the 7 nearest past requests (cosine ≥ 0.1) vote for their label, weighted by
  `label weight × sim²`; weak negatives subtract. Its blend weight grows with the evidence.

If the embedding model is unreachable the learner switches to a built-in, dependency-free **hashing
embedder** (words, bigrams, character trigrams → 512 dims) and rebuilds; vectors of different spaces
are never mixed.

**B · Classifier.** `logit_c = W_c·x + b_c + α·rule_c + β·desc_c` — per-candidate weights over the
request embedding plus two shared weights over the keyword score and the description similarity,
so it can rank agents it has never seen a label for. Trained by SGD (L2, decaying learning rate):
- labels: **corrections** (weight 2, plus a weak negative 0.5 for the replaced agent), **successful
  outcomes** (1), unconfirmed routed tasks (0.2), note `examples:` (0.5);
- **failures, denied/expired approvals and escalations** are weak negatives (0.3);
- minimum-data guard: the classifier's blend weight is `2 × min(1, labels / min_examples)` (20).

Online: every outcome/correction is one SGD step + one kNN entry, and the weights are persisted
(`router_learn_state`). On startup the persisted weights are loaded, then everything is **retrained
from history** (router_decisions ⋈ outcomes, chronological prequential pass + 3 shuffled epochs)
in the background, and again every `rebuild_every` (6h).

**Blend and calibration.** `p = Σ wᵢ·pᵢ / Σ wᵢ` over rules (≤ 1), description (1), kNN (≤ 1.5),
classifier (≤ 2) and LLM (2). The confidence is Platt-scaled:
`σ(A·top + B·margin + D·evidence + C)` where *evidence* is the best absolute similarity (to a
description/example or a past request). Defaults are hand-tuned (A 4, B 3, D 2, C −3.5); with ≥ 30
prequential pairs (each predicted before it was learned) plus random-direction "background"
negatives the parameters are fitted, shrunk towards the defaults.

**LLM only when needed.** The LLM classifier is called only when the calibrated confidence is below
`accept` (0.75) or a trained classifier disagrees with retrieval. If it says search/capture its
route wins; otherwise its pick is blended in as a stage. The command module still asks the user when
the final confidence is below its threshold (0.6).

**No fit.** When the confidence is below `no_fit` (0.3) and no confirmed past request is close
(sim ≥ 0.6), the route gets `params.no_fit = true` and the command bar offers
**"Create a new agent for this"** ([ARCHITECT.md](ARCHITECT.md)).

**C · Runtime bandit.** For an agent with several runtimes (`runtime: auto` → the tier list of
`policies/models.md`, or its own `runtimes:`) the learner picks one per **request cluster**
(spherical k-means on request embeddings, k = clamp(n/15, 1, 8)) with Thompson sampling over
Beta posteriors. Reward ∈ [0, 1] = success − min(0.3, 0.3·cost/0.50 €) − min(0.2, 0.2·latency/10 min)
− 0.5 if corrected − 0.2 if an approval was denied or the task escalated. Clusters with fewer than
3 observations use the agent's pooled posterior; arms get prior pseudo-observations in policy order
(first arm Beta(2,1)), so the policy order holds until data says otherwise. Agents with an
`input_schema` get no hint (it would not validate).

### Runtime hint — integration note for the task engine

The route carries `params.runtime_hint = "codex"` (only when the agent has ≥ 2 candidate runtimes).
The command module copies route params into `CreateTaskRequest.params`, so the engine receives it.
**To use it** (tasks module, not done here): in runtime selection, move `params.runtime_hint` to
the front of `RuntimeCandidates(...)` when it is in the list, and drop the key from the params
before input-schema validation and before the assignment context. The usual fallback on
`rate_limited`/`auth` keeps working. The bandit learns from the runtime that actually ran
(`Task.cost.runtime`, else the `runs.runtime` column, else the hint).

## Feedback loop

`Learner.RecordOutcome(Outcome{DecisionID, TaskID, State, CostEUR, Latency, Corrected, Denied,
Escalated, Runtime})` stores a row in `router_learn_outcomes` and learns it once. It is fed
automatically:
- the event bus: terminal `TaskUpdated` (→ outcome of the decision linked to the task),
  `ESCALATING` updates and denied/expired `Approval`s (→ penalties);
- `router_corrections` (written by `CommandService.CorrectRoute`) is synced every 30 s.

## Logged scores and explainability

Each decision's stages are stored in `router_learn_scores` (`stages` JSON: per stage its weight and
per-candidate probabilities, `blended`, `confidence`, `cluster`, `runtime_hint`, `no_fit`,
`used_llm`), next to the router's own `router_decisions` row (stage `learned`, `learned+llm`,
`llm` or the rule stage). The route reason explains the decision, e.g.

> matched 3 similar past requests (we are out of milk… ×2, order the weekly…) + classifier 0.82; runtime codex (12 past runs)

## Offline evaluation

```
nexusd router eval [-config nexus.yaml] [-hash] [-folds 10]
```

replays the confirmed decisions (corrections and successful outcomes) of the hub database:
leave-one-out up to 300 examples (k-fold above), and prints accuracy, top-2, the logged router's
accuracy, ECE / Brier, the reliability bins, the ask/LLM rate and per-stage accuracy. It uses the
`embed` model (cached vectors are reused) or the hashing embedder with `-hash`.

Synthetic benchmark (tests, hashing embedder, 6 agents whose users' vocabulary is mostly absent
from the descriptions, 25% cross-agent words, 6% label noise):

| labels | 0 | 10 | 40 | 200 |
|---|---|---|---|---|
| held-out accuracy | 0.16 | 0.33 | 0.78 | 0.91 |

Leave-one-out over 150 decisions: accuracy 0.92, top-2 0.96, ECE 0.034, Brier 0.065, LLM/ask rate
0.13 (accuracy 0.96 when confident); description stage alone 0.21, kNN 0.91, classifier 0.93. The
Thompson bandit picks the best of three runtimes (success 0.3 / 0.55 / 0.9, policy order worst
first) in > 85% of rounds after 300 rounds.

## Configuration

```yaml
router.learn:
  enabled: true
  embed_model: embed     # LLM alias, or "hash"
  accept: 0.75           # skip the LLM above this calibrated confidence
  no_fit: 0.3            # offer to create an agent below this
  min_examples: 20
  k: 7
  cost_scale_eur: 0.5
  latency_scale: 10m
  rebuild_every: 6h
  sync_every: 30s
```

Tables (module `router.learn`): `router_learn_vectors`, `router_learn_scores`,
`router_learn_outcomes`, `router_learn_state`. The router's `router_decisions` /
`router_corrections` are unchanged.
