# Connecting models: local Ollama, cloud APIs, any OpenAI- or Anthropic-compatible gateway

Every model call in Nexus goes through one client (`backend/internal/llm`):

- **hub** (`nexusd`, config section `llm`): the command router and the memory/vault Ask pipeline;
- **nodes** (`nexus-node`, config section `node.llm`): the native agent runtimes (`openai`,
  `ollama`, `browser`, …).

Agents and the router never name a provider model. They use **aliases** (`fast-local`, `smart`,
`best`, `embed`, see `vault/policies/models.md`), and the config maps each alias to a provider and
a model, with optional fallbacks.

## 1. Providers

A provider is one HTTP endpoint. Two API flavours are supported:

| `api` | Endpoints used | Typical servers |
|---|---|---|
| `openai` (default) | `POST /chat/completions`, `POST /embeddings`, `GET /models` | Ollama, vLLM, llama.cpp server, LM Studio, LiteLLM, OpenRouter, OpenAI |
| `anthropic` | `POST /messages` (+ `anthropic-version` header), `GET /models` | Anthropic API, gateways that expose the Messages API |

```yaml
llm:
  default_provider: ollama
  providers:
    ollama:
      base_url: http://localhost:11434/v1
      api: openai
      auth: none
      timeout: 300s
    cloud:
      base_url: https://api.anthropic.com
      api: anthropic
      api_key_env: CLOUD_API_KEY
```

**`base_url`** is the API root. If the URL has no path, `/v1` is appended
(`http://192.168.1.20:4000` → `http://192.168.1.20:4000/v1`). A URL with a path is used as given,
so gateways that mount the API under a prefix work (`http://gw.lan/api/openai/v1`). Pasted endpoint
URLs are trimmed (`…/v1/chat/completions` → `…/v1`).

**Auth**

| Setting | Sends |
|---|---|
| `api_key_env: VAR` (openai default `auth: bearer`) | `Authorization: Bearer $VAR` |
| `api: anthropic` (default) | `x-api-key: $VAR` |
| `auth: bearer` on an anthropic provider | `Authorization: Bearer $VAR` |
| `auth_header: X-My-Key` (implies `auth: header`) | `X-My-Key: $VAR` |
| `auth: none` | nothing (LAN servers such as Ollama) |

Keys are only read from the environment; never put them in the YAML. Extra static headers go in
`headers: {…}`.

**TLS.** `https://` endpoints are verified against the system roots. For a gateway with a private
CA or a self-signed certificate, export the CA (or the certificate itself) as PEM and set
`ca_file: /etc/nexus/gateway-ca.pem`. `insecure_skip_verify` is rejected at startup on purpose:
anything on the path could then read your prompts and keys.

Other per-provider options: `timeout` (per request, default 300s), `max_tokens` (default for the
anthropic API, which requires it; 4096), `anthropic_version`, `no_stream_usage` (for
OpenAI-compatible servers that reject `stream_options`).

## 2. Aliases

```yaml
llm:
  aliases_auto: true
  aliases:
    fast-local: { provider: ollama, model: CHANGE_ME }      # picked automatically
    embed:      { provider: ollama, model: "nomic-embed-text" }
    smart:
      provider: cloud
      model: claude-sonnet-4-5
      fallback: [{ provider: ollama, model: "qwen2.5:14b" }]
    best:       { provider: cloud, model: CHANGE_ME }
  alias_preferences:              # optional override of the built-in lists
    fast-local: [qwen2.5:7b, llama3.2]
```

- An **explicit model always wins.**
- With `aliases_auto: true`, an alias whose model is `CHANGE_ME`, empty or `auto` (and the standard
  aliases when they are not configured at all) is filled from the providers' model lists. Each
  alias has an ordered list of substrings; the first substring that matches any model wins, and
  among matches the greatest id (usually the newest version) is chosen. If the alias names a
  provider, only that provider is searched; otherwise the default provider is preferred on ties.

  | alias | built-in preference |
  |---|---|
  | `fast-local` | haiku, mini, flash, llama, qwen, phi, gemma, mistral |
  | `smart` | sonnet, gpt-4.1, gpt-4o, gpt-4, gpt-5, gemini, qwen, llama, mistral |
  | `best` | opus, gpt-5, o3, gpt-4.1, sonnet, gemini, qwen, llama |
  | `embed` | embed (embedding models are only eligible for `embed`, and vice versa) |

  Discovery runs in the background at startup and lazily on first use; a failed discovery is
  retried after 30 s.
- An unknown alias is sent as a raw model name to `default_provider`.

**Fallbacks and health.** Each alias may list `fallback` targets. A request goes to the primary,
then to each fallback when the error could be fixed elsewhere (network errors, timeouts, 5xx, 429,
401/403, unknown model). Malformed requests (400/422) do not fall back. A streaming reply never
falls back once text has been delivered. `embed` only falls back to the **same model name** on
another provider, because vectors from different models are not comparable.

After `health.failure_threshold` (3) consecutive provider failures, the provider is **degraded**:
it is skipped for `health.cooldown` (15 s), doubling on each further failure up to
`health.max_cooldown` (5 m). When the cool-down expires, one request probes it again, and a
success resets everything. If every target is degraded, they are still tried in order. Inside the
code, `(*llm.Client).Status()` returns the per-provider snapshot (failures, cool-down, last error,
latency).

## 3. Checking the setup

```
nexusd llm check  [-config nexus.yaml] [-alias smart] [-quick] [-timeout 90s]
nexusd llm models [-config nexus.yaml] [-provider ollama]
nexusd llm status [-config nexus.yaml]
nexus-node llm check|models|status [-config node.yaml]      # section node.llm
```

`llm check` does the following:
1. For each provider, it checks reachability, TLS and auth, and lists the models.
2. It resolves every alias, including auto-picked ones, and warns when an alias names a model that
   the provider does not list.
3. For every chat target (primary and fallbacks), it runs four probes:
   - a tiny chat ("reply with OK");
   - a tool-calling probe (does the model call a dummy `nexus.probe` tool?);
   - a JSON-mode probe;
   - a streaming probe.
4. It runs an embeddings probe for the `embed` alias.

The command exits with a non-zero status when any step fails. Warnings (`!`) do not fail it.

```
nexusd llm check — nexus.yaml (section llm)

PROVIDER  API     BASE URL                   AUTH
ollama    openai  http://localhost:11434/v1  none

   CHECK             TARGET                             LATENCY  DETAIL
✓  ollama: models    http://localhost:11434/v1/models   3ms      4 models
✓  alias best        ollama/qwen2.5:14b                 -        auto
✓  alias embed       ollama/nomic-embed-text:latest     -        auto
✓  alias fast-local  ollama/llama3.2:3b                 -        auto
✓  alias smart       ollama/qwen2.5:14b                 -        auto
✓  chat              ollama/qwen2.5:14b                 1.42s    "OK", 24+2 tokens
✓  tools             ollama/qwen2.5:14b                 820ms    nexus.probe({"word":"ping"}), stop: tool_calls
✓  json              ollama/qwen2.5:14b                 610ms    {"ok": true, "n": 3}
✓  stream            ollama/qwen2.5:14b                 540ms    9 deltas, first after 180ms
!  tools             ollama/llama3.2:3b                 300ms    answered without calling the tool (stop: stop) [1]
…
✓  embed             ollama/nomic-embed-text:latest     45ms     dim 768

hints:
  [1] native agents need tool calling; pick a tool-capable model for this alias (…)

15 ok, 1 warnings, 0 failed, 0 skipped
```

## 4. Hub and nodes on the LAN or a VPN

Both the hub and every node talk to model servers directly; nothing is proxied through the hub.
Use an address that each machine can reach. Examples: `http://gpu-box.lan:11434/v1`, a Tailscale
MagicDNS name such as `http://gpu-box.tailnet-name.ts.net:11434/v1`, or a fixed IP. On the model
server:

- **Ollama** listens on localhost only by default. Start it with `OLLAMA_HOST=0.0.0.0` so it
  accepts LAN clients, and allow port 11434 in the firewall *only* on the LAN/VPN interface.
- **A gateway you run (LiteLLM, vLLM, …)**: point `base_url` at its OpenAI root (usually `…/v1`),
  or use `api: anthropic` if it exposes `/v1/messages`. Put a key on it (`api_key_env`) even on
  the LAN, and use HTTPS with `ca_file` if it crosses networks you do not control.

Run `nexusd llm check` on the hub and `nexus-node llm check` on each node. Different machines may
see different addresses.

## 5. Troubleshooting

| Symptom in `llm check` | Fix |
|---|---|
| `connection refused` | The server is not running, or the port is wrong, or it only listens on localhost (Ollama: `OLLAMA_HOST=0.0.0.0`). |
| `cannot resolve "host"` | Use an IP or a name this machine resolves (`.lan`, Tailscale MagicDNS). |
| `timeout` | Firewall or VPN is down, or a large model is still loading. Raise `-timeout` or `timeout:`. |
| `HTTP 404 … base_url is probably wrong` | Add or remove `/v1`, or include the gateway's path prefix. Check the URL shown in the hint. |
| `HTML page — … web UI` | `base_url` points at a dashboard, not the API. |
| `HTTP 401/403` | `$VAR is empty`: export the key for the process (the systemd unit or launchd plist too). `was rejected`: wrong key, or the server wants a different header. Try `auth: bearer` or `auth_header: …`. |
| `TLS verification failed` | Set `ca_file` to the gateway's CA/self-signed certificate (PEM). |
| `certificate does not cover this host` | Use the host name in the certificate's SANs in `base_url`. |
| `the server speaks plain HTTP` | Use `http://`. |
| `model … is not served` | Copy the exact id from `nexusd llm models` (Ollama tags such as `qwen2.5:7b` matter). |
| tools `!` answered without calling the tool | That model cannot drive agents. Use a tool-capable model for `smart`/`best`. |
| json `!` / json `✗` HTTP 400 | The server ignores or rejects `response_format`. Choose another model for the router's `classifier` alias. |
| stream `!` arrived in one piece | A reverse proxy is buffering SSE (nginx: `proxy_buffering off`), or the server does not stream. |
| alias `✗ no model` | Nothing matched the preferences. Pull a model, set `model:` explicitly, or extend `alias_preferences`. |

## 6. Privacy

With only local providers (the default config: Ollama on this machine or on your LAN/VPN), **no
prompt, document or embedding leaves your network**. Router classification, memory search,
vault Ask and native agents all run on your hardware. A cloud provider is used only for aliases
that point at it, or that fall back to it. Keep `fast-local` and `embed` local so that
classification and indexing of your vault stay at home even when `smart`/`best` use the cloud. If
a gateway routes to cloud models itself, Nexus cannot see that. Check the gateway's routing
config.
