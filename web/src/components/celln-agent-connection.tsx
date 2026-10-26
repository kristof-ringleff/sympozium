import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, type Agent, type ModelConnection } from "@/lib/api";
import { useCellnMediation, useCellnPlatformProfiles, useModelConnections, usePatchAgent } from "@/hooks/use-api";
import { enduringForOutputTokens, keyChoiceReady, managedSecretName, offersThinkingSwitch, routeForConnection, type KeyChoice } from "@/lib/celln-own-key";
import { denyingPattern, originStatus } from "@/lib/celln-providers";
import { DEFAULT_MAX_OUTPUT_TOKENS, parseMaxOutputTokens, parseModelParameters } from "@/lib/model-parameters";
import { CellnKeyStep } from "@/components/celln-own-key";
import { CellnModelParametersField, CellnModelParametersSummary } from "@/components/celln-model-parameters";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";

/**
 * The model backend a Celln Agent owns: its provider, model, endpoint and the
 * name of its Secret (never the value), with an edit for the model parameters,
 * the output-token limit and the key. Saving rewrites this Agent's
 * ModelConnection only; no other Agent shares it. The connection is the route;
 * the namespace's policy is shown only where it would refuse the origin.
 */
export function CellnAgentConnection({ agent }: { agent: Agent }) {
  const qc = useQueryClient();
  const connections = useModelConnections();
  const mediation = useCellnMediation();
  const profiles = useCellnPlatformProfiles();
  const patchAgent = usePatchAgent();
  const execution = agent.spec.execution;
  const connection = (connections.data || []).find((candidate) => candidate.metadata.name === execution?.modelConnectionRef);
  const [editing, setEditing] = useState(false);
  const [parameters, setParameters] = useState("");
  const [maxOutputTokens, setMaxOutputTokens] = useState("");
  const [replaceKey, setReplaceKey] = useState(false);
  const [key, setKey] = useState<KeyChoice>({ mode: "create", apiKey: "" });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);

  if (connections.isLoading) return null;
  if (!connection) {
    return (
      <div className="space-y-1 rounded-md border p-3 text-xs text-muted-foreground" data-testid="agent-model-backend">
        <Label>Model backend</Label>
        <p>{execution?.modelConnectionRef ? <>Model connection <code>{execution.modelConnectionRef}</code> does not exist in this namespace, so this Agent's runs are refused.</> : "This Agent names no model connection."} A Celln Agent owns its model backend; create one with Create Agent → Celln.</p>
      </div>
    );
  }
  const model = execution?.model || connection.spec.models[0];
  const route = routeForConnection(connection, model);
  const origin = route.endpointOrigins[0];
  const verdict = mediation.data?.enabled ? originStatus(origin, mediation.data) : undefined;
  const denied = mediation.data ? denyingPattern(origin, mediation.data) : undefined;
  const runtimeRef = agent.spec.runtimeRef || execution?.cellnSelection?.runtimeRef || "";
  const profile = (profiles.data || []).find((candidate) => candidate.wrapper === runtimeRef);
  const parsedParameters = parseModelParameters(parameters);
  const parsedTokens = parseMaxOutputTokens(maxOutputTokens);
  const secretKey = route.secretKey || (connection.spec.protocol === "anthropic-messages" ? "ANTHROPIC_API_KEY" : "OPENAI_API_KEY");

  function startEditing(current: ModelConnection) {
    setParameters(current.spec.parameters && Object.keys(current.spec.parameters).length ? JSON.stringify(current.spec.parameters, null, 2) : "");
    setMaxOutputTokens(current.spec.maxOutputTokens ? String(current.spec.maxOutputTokens) : "");
    setReplaceKey(false);
    setKey({ mode: "create", apiKey: "" });
    setError("");
    setSaved(false);
    setEditing(true);
  }

  async function save(current: ModelConnection) {
    if (saving || parsedParameters.error || parsedTokens.error || (replaceKey && !keyChoiceReady(key))) return;
    setSaving(true);
    setError("");
    // The whole spec is sent again: the API replaces it, and keeps the route.
    // Its refusals are shown in its own words, in particular the 409 when the
    // Secret is already pinned to another origin.
    const { parameters: _oldParameters, maxOutputTokens: _oldTokens, secretRef, ...fixed } = current.spec;
    const pasted = replaceKey && key.mode === "create";
    const nextSecret = replaceKey && key.mode === "existing" ? key.secretName : secretRef;
    try {
      await api.modelConnections.create({
        name: current.metadata.name,
        spec: {
          ...fixed,
          // With a pasted key the API writes the Secret and names it itself.
          ...(pasted ? {} : { secretRef: nextSecret }),
          ...(parsedParameters.parameters ? { parameters: parsedParameters.parameters } : {}),
          ...(parsedTokens.maxOutputTokens ? { maxOutputTokens: parsedTokens.maxOutputTokens } : {}),
        },
        apiKey: pasted && key.mode === "create" ? key.apiKey.trim() : undefined,
      });
    } catch (err) {
      setSaving(false);
      setError(`The model connection was not changed: ${err instanceof Error ? err.message : "request failed"}`);
      return;
    }
    // The pasted key is in the cluster now; keep no copy of it here.
    setKey({ mode: "create", apiKey: "" });
    qc.invalidateQueries({ queryKey: ["model-connections"] });
    qc.invalidateQueries({ queryKey: ["celln-key-secrets"] });
    try {
      // Re-saving the execution defaults moves the Agent's authRefs grant to
      // the connection's Secret and resizes a conversation's budget, because a
      // turn reserves six requests of the output-token limit.
      if (execution) {
        await patchAgent.mutateAsync({ name: agent.metadata.name, data: { execution: { ...execution, ...(execution.executionLifecycle === "enduring" && profile ? { enduring: enduringForOutputTokens(profile, parsedTokens.maxOutputTokens) } : {}) } } });
      }
      setEditing(false);
      setSaved(true);
    } catch (err) {
      setError(`The model connection was saved, but the Agent was not updated to match it (its Secret grant and conversation budget): ${err instanceof Error ? err.message : "request failed"}. Save again to retry.`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="space-y-3 rounded-md border p-3" data-testid="agent-model-backend">
      <div className="flex items-center justify-between gap-2">
        <Label>Model backend (this Agent's own)</Label>
        {!editing && <Button type="button" size="sm" variant="outline" data-testid="agent-model-backend-edit" onClick={() => startEditing(connection)}>Edit</Button>}
      </div>
      <dl className="grid grid-cols-[8rem_1fr] gap-x-3 gap-y-1 text-xs">
        <dt className="text-muted-foreground">Provider</dt><dd>{connection.spec.provider} <span className="text-muted-foreground">({connection.spec.protocol})</span></dd>
        <dt className="text-muted-foreground">Model</dt><dd className="font-mono">{model}</dd>
        <dt className="text-muted-foreground">Endpoint origin</dt><dd className="break-all font-mono" title={connection.spec.endpoint}>{origin}{connection.spec.allowInsecure ? <span className="font-sans text-muted-foreground"> (allowInsecure)</span> : null}</dd>
        <dt className="text-muted-foreground">Secret</dt><dd>{connection.spec.secretRef ? <><code data-testid="agent-model-backend-secret">{connection.spec.secretRef}</code> <span className="text-muted-foreground">holding {secretKey}, pinned to this origin; the value is never shown</span></> : "Not required — keyless endpoint"}</dd>
        <dt className="text-muted-foreground">Connection</dt><dd><code>{connection.metadata.name}</code></dd>
        <dt className="text-muted-foreground">Parameters</dt><dd>{connection.spec.parameters && Object.keys(connection.spec.parameters).length ? <CellnModelParametersSummary parameters={connection.spec.parameters} testId="agent-model-backend-parameters" /> : <span className="text-muted-foreground">none</span>}</dd>
        <dt className="text-muted-foreground">Max output tokens</dt><dd data-testid="agent-model-backend-tokens">{connection.spec.maxOutputTokens || DEFAULT_MAX_OUTPUT_TOKENS} per request{connection.spec.maxOutputTokens ? "" : " (default)"}</dd>
      </dl>
      {mediation.data && !mediation.data.enabled && (
        <p role="alert" className="text-xs text-red-400" data-testid="agent-model-backend-mediation-disabled">
          Mediated model access is disabled on this cluster (no model gateway), so this Agent's requests to {origin} cannot leave a cell and its runs are held.
        </p>
      )}
      {mediation.data && verdict === "denied" && (
        <p role="alert" className="break-words text-xs text-red-400" data-testid="agent-model-backend-origin-denied">
          Policy {mediation.data.policies.join(", ") || "for this namespace"} does not admit origin {origin}{denied ? ` (denied pattern ${denied})` : ""}: allowed {mediation.data.allowedOrigins.join(", ") || "any public origin"}; denied {mediation.data.deniedOrigins.join(", ") || "none"}. Runs on this connection are refused AUTH_ROUTE_MISMATCH until the policy admits it.
        </p>
      )}
      {mediation.data && verdict === "needs-approval" && (
        <p role="alert" className="break-words text-xs text-amber-500" data-testid="agent-model-backend-origin-needs-approval">
          {origin} is a private, LAN or loopback endpoint. The model gateway reaches it only once an operator adds exactly <code>{origin}</code> to <code>modelGateway.privateOrigins</code>; until then runs on this connection are refused.
        </p>
      )}
      {saved && !editing && <p role="status" className="text-xs text-emerald-400">Saved. Start a new conversation to use it.</p>}
      {editing && (
        <div className="space-y-3 border-t pt-3">
          <CellnModelParametersField value={parameters} onChange={setParameters} showThinking={offersThinkingSwitch(connection.spec)} maxOutputTokens={maxOutputTokens} onMaxOutputTokensChange={setMaxOutputTokens} />
          {connection.spec.secretRef && <label className="flex items-center gap-2 text-xs">
            <input type="checkbox" data-testid="agent-model-backend-replace-key" checked={replaceKey} onChange={(e) => setReplaceKey(e.target.checked)} />
            Replace the key
          </label>}
          {replaceKey && <CellnKeyStep route={route} value={key} onChange={setKey} managedSecretName={managedSecretName(connection.metadata.name, connection.spec.provider)} />}
          <p className="text-xs text-amber-500" data-testid="agent-model-backend-route-changed">
            A conversation already running on this Agent is pinned to the connection as it was. After a change to the parameters, the output-token limit or the Secret it names, the model gateway refuses that conversation's next request (MODEL_ROUTE_CHANGED): start a new conversation. The provider, model and endpoint cannot be edited; create another Agent for those. A key stays pinned to the origin it was first used with: the API refuses to move it elsewhere.
          </p>
          {error && <p role="alert" className="break-words text-xs text-red-400">{error}</p>}
          <div className="flex gap-2">
            <Button type="button" size="sm" data-testid="agent-model-backend-save" disabled={saving || !!parsedParameters.error || !!parsedTokens.error || (replaceKey && !keyChoiceReady(key))} onClick={() => save(connection)}>{saving ? "Saving…" : "Save"}</Button>
            <Button type="button" size="sm" variant="ghost" disabled={saving} onClick={() => { setEditing(false); setKey({ mode: "create", apiKey: "" }); }}>Cancel</Button>
          </div>
        </div>
      )}
    </div>
  );
}
