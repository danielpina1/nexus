import { createClient, type Client, type Interceptor, type Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import type { DescService } from "@bufbuild/protobuf";
import type { AgentService } from "../gen/nexus/v1/agent_pb";
import { ApprovalService } from "../gen/nexus/v1/approval_pb";
import { CommandService } from "../gen/nexus/v1/command_pb";
import type { DeviceService } from "../gen/nexus/v1/device_pb";
import { EventService } from "../gen/nexus/v1/event_pb";
import { NodeService } from "../gen/nexus/v1/node_pb";
import { TaskService } from "../gen/nexus/v1/task_pb";
import type { VaultService } from "../gen/nexus/v1/vault_pb";
import { getSettings } from "./settings";

/**
 * The dashboard talks ONLY to the Nexus Hub API. Node files, node status and agent runs are all
 * reached through Hub services; there is never a direct connection to nodes or model providers.
 */

export interface Clients {
  agents: Client<typeof AgentService>;
  approvals: Client<typeof ApprovalService>;
  command: Client<typeof CommandService>;
  devices: Client<typeof DeviceService>;
  events: Client<typeof EventService>;
  nodes: Client<typeof NodeService>;
  tasks: Client<typeof TaskService>;
  vault: Client<typeof VaultService>;
}

/** Adds `Authorization: Bearer <token>` from settings to every request. */
const authInterceptor: Interceptor = (next) => async (req) => {
  const { token } = getSettings();
  if (token) req.header.set("Authorization", `Bearer ${token}`);
  return next(req);
};

export function hubBaseUrl(): string {
  const { hubUrl } = getSettings();
  const trimmed = hubUrl.trim().replace(/\/+$/, "");
  return trimmed || window.location.origin;
}

function createHubTransport(): Transport {
  return createConnectTransport({
    baseUrl: hubBaseUrl(),
    interceptors: [authInterceptor],
  });
}

/**
 * A client whose service descriptor is code-split: the generated file (vault_pb is ~90 KB of
 * descriptors and growing) loads on the first call instead of in the main chunk. Only for services
 * whose methods are all unary.
 */
function lazyClient<S extends DescService>(load: () => Promise<S>, transport: Transport): Client<S> {
  let real: Promise<Client<S>> | null = null;
  const get = () => (real ??= load().then((svc) => createClient(svc, transport)));
  return new Proxy({} as Client<S>, {
    get: (_t, method: string) => (req: unknown, opts?: unknown) =>
      get().then((c) => (c as unknown as Record<string, (r: unknown, o?: unknown) => Promise<unknown>>)[method](req, opts)),
  });
}

function makeClients(transport: Transport): Clients {
  return {
    agents: lazyClient(async () => (await import("../gen/nexus/v1/agent_pb")).AgentService, transport),
    approvals: createClient(ApprovalService, transport),
    command: createClient(CommandService, transport),
    devices: lazyClient(async () => (await import("../gen/nexus/v1/device_pb")).DeviceService, transport),
    events: createClient(EventService, transport),
    nodes: createClient(NodeService, transport),
    tasks: createClient(TaskService, transport),
    vault: lazyClient(async () => (await import("../gen/nexus/v1/vault_pb")).VaultService, transport),
  };
}

/** Checks a hub URL + token without touching the app's settings (used by the Connect screen). */
export async function probeHub(hubUrl: string, token: string): Promise<void> {
  const baseUrl = hubUrl.trim().replace(/\/+$/, "") || window.location.origin;
  const transport = createConnectTransport({
    baseUrl,
    interceptors: [
      (next) => async (req) => {
        req.header.set("Authorization", `Bearer ${token}`);
        return next(req);
      },
    ],
  });
  await createClient(NodeService, transport).listNodes({}, { timeoutMs: 8000 });
}

let clients: Clients | null = null;
let mockActive = false;

/** Builds the transport. Mock mode lazy-loads the in-memory hub so it never ships in the main chunk. */
export async function initApi(): Promise<void> {
  const { mock } = getSettings();
  let transport: Transport;
  if (mock) {
    const { createMockTransport } = await import("../mock/transport");
    transport = createMockTransport([authInterceptor]);
    mockActive = true;
  } else {
    transport = createHubTransport();
    mockActive = false;
  }
  clients = makeClients(transport);
}

/** Rebuilds the real transport after the hub URL changes (mock changes require a reload). */
export function reconfigureApi(): void {
  if (mockActive) return;
  clients = makeClients(createHubTransport());
}

export function api(): Clients {
  if (!clients) throw new Error("api() called before initApi()");
  return clients;
}

export function isMock(): boolean {
  return mockActive;
}
