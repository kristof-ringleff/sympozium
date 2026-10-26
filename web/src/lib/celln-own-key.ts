// A native Celln Agent owns its model backend: a namespaced ModelConnection
// backed by a Secret in the Agent's namespace. The connection is the route —
// any provider, any OpenAI- or Anthropic-compatible endpoint — and the
// namespace's policy only guards the endpoint origin
// (docs/guides/celln-mediated-model-access.md). One key belongs to one Agent
// and is pinned to the first origin it is used with; the API refuses to move it.
// Nothing here ever keeps an API key: it is handed to the API once and dropped.
import { ApiError, api } from "@/lib/api";
import type { CellnMediatedRoute, CellnPlatformProfile, EnduringLimits, ModelConnection } from "@/lib/api";
import { turnOutputTokens, type ModelParameters } from "@/lib/model-parameters";
import { TURN_MODEL_REQUESTS } from "@/lib/agent-execution";
import { cellnPreset, endpointOrigin, secretKeyFor } from "@/lib/celln-providers";

export { routeAllowsModel, routeModelsLabel, validModelName } from "@/lib/celln-routes";

/** Create a Secret from a pasted key, or name one that already holds it. */
export type KeyChoice = { mode: "create"; apiKey: string } | { mode: "existing"; secretName: string };

/** The guide to an Agent's own model access through the gateway: docs/guides/celln-mediated-model-access.md. */
export const GUIDE_URL = "https://github.com/sympozium-ai/sympozium/blob/main/docs/guides/celln-mediated-model-access.md";

/** The model a route starts with: its only model, the provider preset's well-known default, or nothing to choose. */
export function initialModelFor(route: CellnMediatedRoute): string {
  if (route.anyModel) return cellnPreset(route.provider)?.suggestedModel || "";
  return route.models.length === 1 ? route.models[0] : "";
}

/** The standard request path of a protocol; a provider or gateway may differ. */
export function defaultEndpointPath(protocol: string): string {
  return protocol === "anthropic-messages" ? "/v1/messages" : "/v1/chat/completions";
}

/** The Secret the API writes for a pasted key (apiserver defaultProviderSecretName). */
export function managedSecretName(connectionName: string, provider: string): string {
  return `${connectionName}-${provider.trim().toLowerCase()}-key`;
}

/**
 * Whether the chat-template "no thinking" switch applies: servers such as
 * llama-server or vLLM behind the OpenAI protocol take it; the hosted OpenAI
 * and Anthropic APIs refuse unknown request fields.
 */
export function offersThinkingSwitch(route: { provider: string; protocol: string }): boolean {
  return route.protocol === "openai-chat" && !["openai", "azure-openai"].includes(route.provider);
}

export function keyChoiceReady(choice: KeyChoice): boolean {
  return choice.mode === "create" ? choice.apiKey.trim().length > 0 : choice.secretName.length > 0;
}

export interface OwnKeySelection {
  route: CellnMediatedRoute;
  origin: string;
  /** Request path on the origin, e.g. /v1/messages. */
  path: string;
  model: string;
  parameters?: ModelParameters;
  maxOutputTokens?: number;
}

/** The connection spec for a selection; the Secret reference is added by the caller or the API. */
export function ownKeyConnectionSpec(selection: OwnKeySelection, secretRef?: string): ModelConnection["spec"] {
  return {
    provider: selection.route.provider,
    protocol: selection.route.protocol,
    endpoint: `${selection.origin}${selection.path.startsWith("/") ? "" : "/"}${selection.path}`,
    models: [selection.model],
    ...(secretRef && selection.route.auth !== "none" ? { secretRef } : {}),
    ...(selection.route.allowInsecure ? { allowInsecure: true } : {}),
    ...(selection.parameters && Object.keys(selection.parameters).length ? { parameters: selection.parameters } : {}),
    ...(selection.maxOutputTokens ? { maxOutputTokens: selection.maxOutputTokens } : {}),
  };
}

/**
 * The route a connection is: its own provider, protocol, models and endpoint
 * origin, keyed by the Secret it names (under the key the connection or its
 * protocol fixes). The origin falls back to the endpoint text when unreadable.
 */
export function routeForConnection(connection: ModelConnection, model?: string): CellnMediatedRoute {
  const { spec } = connection;
  const models = model && !spec.models.includes(model) ? [...spec.models, model] : spec.models;
  return {
    provider: spec.provider,
    protocol: spec.protocol,
    models,
    endpointOrigins: [endpointOrigin(spec.endpoint) || spec.endpoint],
    auth: spec.secretRef ? "secret" : "none",
    secretKey: spec.secretRef ? spec.secretKey || secretKeyFor(spec.protocol) : "",
    ...(spec.allowInsecure ? { allowInsecure: true } : {}),
  };
}

/**
 * The budget one conversation asks for. A turn reserves 6 requests of the
 * connection's output cap, so a raised cap buys fewer turns under the policy's
 * ceilings (cellninstall.SessionDefaultsAt does the same sum for a profile).
 */
export function enduringForOutputTokens(profile: CellnPlatformProfile, maxOutputTokens?: number): EnduringLimits {
  if (!maxOutputTokens) return { ...profile.sessionDefaults };
  const turnTokens = turnOutputTokens(maxOutputTokens);
  const { ceilings, sessionDefaults } = profile;
  const turns = Math.max(1, Math.min(sessionDefaults.maxTurns, Math.floor(ceilings.maxModelRequests / TURN_MODEL_REQUESTS), Math.floor(ceilings.maxOutputTokens / turnTokens)));
  return {
    leaseSeconds: sessionDefaults.leaseSeconds,
    maxTurns: turns,
    maxModelRequests: Math.min(turns * TURN_MODEL_REQUESTS, ceilings.maxModelRequests),
    maxOutputTokens: Math.min(turns * turnTokens, ceilings.maxOutputTokens),
  };
}

export type OwnKeyStepName = "secret" | "connection" | "runtime" | "agent";
export type OwnKeyStepState = "done" | "failed" | "not-started";

export interface OwnKeyStep {
  step: OwnKeyStepName;
  /** The object's kind and name, e.g. `Secret my-agent-connection-anthropic-key`. */
  object: string;
  state: OwnKeyStepState;
  /** "created", "already there", … — never a credential. */
  note?: string;
}

export class OwnKeyError extends Error {
  steps: OwnKeyStep[];
  constructor(message: string, steps: OwnKeyStep[]) {
    super(message);
    this.name = "OwnKeyError";
    this.steps = steps;
  }
}

/**
 * What an Agent with its own key runs on and lends: the backend's toolbox and
 * exactly its tools, in order, when the scope publishes one; otherwise the
 * backend's own runtime and no tools (on the gateway-mediated path that
 * runtime lends none).
 */
export function ownKeySelection(profile: CellnPlatformProfile): { profile: string; wrapper: string; tools: { name: string; revision: string }[] } {
  if (profile.toolboxProfile && profile.toolboxWrapper && profile.toolboxTools?.length) {
    return { profile: profile.toolboxProfile, wrapper: profile.toolboxWrapper, tools: profile.toolboxTools.map((tool) => ({ ...tool })) };
  }
  return { profile: profile.name, wrapper: profile.wrapper, tools: [] };
}

/**
 * Saves the Agent's Secret (when pasted) and ModelConnection, then makes sure
 * the namespace has the fleet's runtime wrapper (ownKeySelection's). Every call is idempotent, so
 * a retry after a failure repeats nothing harmful. Throws OwnKeyError naming
 * what exists and what does not; the API's own words are kept, in particular
 * its 409 when a Secret is already pinned to another origin.
 */
export async function prepareOwnKeyBackend(input: { connectionName: string; selection: OwnKeySelection; key: KeyChoice; profile: CellnPlatformProfile; agentName?: string; onConnectionSaved?: (connection: ModelConnection) => void }): Promise<{ connection: ModelConnection; runtime: string; steps: OwnKeyStep[] }> {
  const { connectionName, selection, key, profile } = input;
  const keyless = selection.route.auth === "none";
  const creating = !keyless && key.mode === "create";
  const secretName = keyless ? "" : key.mode === "create" ? managedSecretName(connectionName, selection.route.provider) : key.secretName;
  const steps: OwnKeyStep[] = [
    ...(!keyless ? [{ step: "secret" as const, object: `Secret ${secretName}`, state: creating ? "not-started" as const : "done" as const, note: creating ? undefined : "existing Secret, linked" }] : []),
    { step: "connection", object: `ModelConnection ${connectionName}`, state: "not-started" },
    { step: "runtime", object: `AgentRuntime ${ownKeySelection(profile).wrapper}`, state: "not-started" },
    ...(input.agentName ? [{ step: "agent" as const, object: `Agent ${input.agentName}`, state: "not-started" as const }] : []),
  ];
  const mark = (step: OwnKeyStepName, state: OwnKeyStepState, note?: string) => {
    const entry = steps.find((candidate) => candidate.step === step);
    if (entry) Object.assign(entry, { state, note });
  };

  let connection: ModelConnection;
  try {
    connection = await api.modelConnections.create({
      name: connectionName,
      spec: ownKeyConnectionSpec(selection, creating ? undefined : secretName),
      apiKey: creating ? key.apiKey.trim() : undefined,
    });
  } catch (err) {
    const message = err instanceof Error ? err.message : "Could not save the model connection";
    // The API validates before it writes anything (400) and writes the Secret
    // before the connection, so any other failure is the connection's.
    const secretFailed = creating && /failed to create credentials secret/i.test(message);
    const refusedBeforeWriting = err instanceof ApiError && err.status === 400;
    if (secretFailed) mark("secret", "failed");
    else {
      if (creating && !refusedBeforeWriting) mark("secret", "done", "written");
      mark("connection", "failed");
    }
    throw new OwnKeyError(message, steps);
  }
  if (creating) mark("secret", "done", "written");
  mark("connection", "done", "saved");
  const connectionStep = steps.find((entry) => entry.step === "connection");
  if (connectionStep) connectionStep.object = `ModelConnection ${connection.metadata.name}`;
  if (creating && connection.spec.secretRef) steps[0].object = `Secret ${connection.spec.secretRef}`;
  // Drop the pasted key before the independently failing runtime step.
  input.onConnectionSaved?.(connection);

  try {
    const wrappers = await api.cellnPlatform.ensureRuntime(ownKeySelection(profile).profile);
    const entry = steps.find((candidate) => candidate.step === "runtime");
    if (entry) entry.object = `AgentRuntime ${wrappers.runtime}`;
    mark("runtime", "done", wrappers.created.includes(wrappers.runtime) ? "created" : "already there");
    return { connection, runtime: wrappers.runtime, steps };
  } catch (err) {
    mark("runtime", "failed");
    throw new OwnKeyError(err instanceof Error ? err.message : "Could not prepare this namespace's Celln runtime", steps);
  }
}
