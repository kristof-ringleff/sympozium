import { useState, type ComponentType, type ReactNode } from "react";
import { Bot, Check, Copy, Loader2, RefreshCw } from "lucide-react";
import { getNamespace, type CellnMediatedRoute, type CellnMediation } from "@/lib/api";
import { useCellnKeySecrets } from "@/hooks/use-api";
import { GUIDE_URL, offersThinkingSwitch, validModelName, type KeyChoice, type OwnKeyStep } from "@/lib/celln-own-key";
import { CELLN_PROVIDERS, cellnPreset, denyingPattern, endpointOrigin, originStatus, routeFromSelection, selectionForPreset, type CellnProtocol, type CellnProviderSelection } from "@/lib/celln-providers";
import { CellnModelParametersField } from "@/components/celln-model-parameters";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { cn } from "@/lib/utils";

export interface ProviderChoice {
  value: string;
  label: string;
  icon: ComponentType<{ className?: string }>;
}

/** Why no provider can be chosen: the release runs no model gateway, so no Agent's key could leave a cell. */
function MediationDisabled() {
  return (
    <div role="status" className="space-y-2 rounded-lg border border-amber-500/30 bg-amber-500/5 p-3 text-xs text-muted-foreground" data-testid="celln-mediation-disabled">
      <p className="text-sm font-medium text-foreground">Mediated model access is not enabled on this cluster</p>
      <p>A Celln Agent brings its own provider key, which only the model gateway adds to its requests; a cell never holds it. This release runs no model gateway (<code>celln.mediation.enabled=false</code>: installed with <code>--no-celln-mediation</code>, or by a source build without a gateway image), so no Celln Agent can reach a model. An operator reruns <code>sympozium install</code> without the opt-out (a source build adds <code>--model-gateway-image</code>).</p>
      <p>See <a className="underline" href={GUIDE_URL} target="_blank" rel="noreferrer">Model access for Celln Agents</a>. <code>sympozium doctor</code> reports whether mediated model access is on.</p>
    </div>
  );
}

/** What the namespace's policy says about the chosen origin, when it is not simply allowed. */
function OriginVerdict({ origin, mediation }: { origin: string; mediation: CellnMediation }) {
  const status = originStatus(origin, mediation);
  const policies = mediation.policies.length ? mediation.policies.map((policy) => <code key={policy}>{policy}</code>).reduce<ReactNode[]>((all, node, index) => (index ? [...all, ", ", node] : [node]), []) : "this namespace's execution policy";
  if (status === "denied") {
    const pattern = denyingPattern(origin, mediation);
    return (
      <div role="alert" className="space-y-1 rounded-lg border border-red-500/30 bg-red-500/5 p-3 text-xs text-muted-foreground" data-testid="celln-origin-denied">
        <p className="break-all font-medium text-red-400">Policy {policies} does not admit <code>{origin}</code>{pattern ? <>: it matches the denied pattern <code>{pattern}</code></> : null}.</p>
        <p className="break-words">Allowed origins: <code>{mediation.allowedOrigins.length ? mediation.allowedOrigins.join(", ") : "any public origin"}</code>. Denied origins: <code>{mediation.deniedOrigins.length ? mediation.deniedOrigins.join(", ") : "none"}</code>. Choose a provider on an admitted origin, or ask an operator to change the policy's <code>allowedOrigins</code>/<code>deniedOrigins</code>. A run on this origin would be refused.</p>
      </div>
    );
  }
  if (status === "needs-approval") {
    return (
      <div role="alert" className="space-y-1 rounded-lg border border-amber-500/30 bg-amber-500/5 p-3 text-xs text-muted-foreground" data-testid="celln-origin-needs-approval">
        <p className="break-all font-medium text-amber-500"><code>{origin}</code> is a private, LAN or loopback endpoint that needs an operator's approval.</p>
        <p className="break-words">The model gateway carries an Agent's key to a private address only when an operator lists exactly this origin in the chart value <code>modelGateway.privateOrigins</code> (<code>helm upgrade … --set 'modelGateway.privateOrigins={"{"}{origin}{"}"}'</code>) — the origin alone, without a path. Until then a run on it would be refused. Approved so far: <code>{mediation.privateOrigins?.length ? mediation.privateOrigins.join(", ") : "none"}</code>.</p>
      </div>
    );
  }
  return null;
}

/**
 * The Provider step of a Celln Agent: the Agent's own connection is its route,
 * so any provider preset, local server or custom endpoint can be chosen. The
 * namespace's policy only guards the endpoint origin, and that verdict is shown
 * here before anything is created. Without a model gateway nothing is offered.
 */
export function CellnRouteStep({ mediation, isLoading, error, providers, selection, onSelect }: {
  mediation?: CellnMediation;
  isLoading: boolean;
  error?: string;
  providers: ProviderChoice[];
  selection?: CellnProviderSelection;
  onSelect: (selection: CellnProviderSelection) => void;
}) {
  if (isLoading) return <p className="flex items-center gap-2 text-xs text-muted-foreground"><Loader2 className="h-3.5 w-3.5 animate-spin" /> Reading this namespace's model access policy…</p>;
  if (error || !mediation) return <p role="alert" className="break-words text-xs text-red-400" data-testid="celln-mediation-error">Could not read the model access policy: {error || "no answer"}</p>;
  if (!mediation.enabled) return <MediationDisabled />;
  const preset = cellnPreset(selection?.preset);
  const route = routeFromSelection(selection, mediation);
  const origin = route?.endpointOrigins[0] || "";
  const typed = preset && preset.kind !== "hosted";
  const iconFor = (value: string) => providers.find((provider) => provider.value === value)?.icon || cellnPreset(value)?.icon || Bot;
  const group = (kind: "hosted" | "local" | "custom", title: string) => (
    <div className="space-y-1.5">
      <p className="text-[11px] uppercase tracking-wider text-muted-foreground">{title}</p>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {CELLN_PROVIDERS.filter((candidate) => candidate.kind === kind).map((candidate) => {
          const Icon = iconFor(candidate.value);
          const active = selection?.preset === candidate.value;
          return (
            <button
              key={candidate.value}
              type="button"
              data-testid={`celln-provider-${candidate.value}`}
              data-provider={candidate.value}
              aria-pressed={active}
              onClick={() => { if (!active) onSelect(selectionForPreset(candidate)); }}
              className={cn("flex items-center gap-2 rounded-md border px-2.5 py-2 text-left text-xs transition-colors", active ? "border-blue-500/40 bg-blue-500/15 text-blue-300" : "border-border/50 hover:bg-white/5")}
            >
              <Icon className="h-4 w-4 shrink-0" />
              <span className="truncate">{providers.find((provider) => provider.value === candidate.value)?.label || candidate.label}</span>
            </button>
          );
        })}
      </div>
    </div>
  );
  return (
    <div className="max-h-[60vh] space-y-4 overflow-y-auto pr-1" data-testid="celln-route-step">
      <div className="space-y-3" data-testid="celln-provider-grid">
        <Label>AI Provider</Label>
        {group("hosted", "Hosted")}
        {group("local", "Local or self-hosted")}
        {group("custom", "Anything else")}
      </div>
      {preset && typed && selection && (
        <div className="space-y-3 rounded-md border border-border/50 p-3" data-testid="celln-endpoint-fields">
          <div className="space-y-1.5">
            <Label htmlFor="celln-endpoint">{preset.kind === "custom" ? "Endpoint URL" : `${preset.label} endpoint`}</Label>
            <Input id="celln-endpoint" data-testid="celln-endpoint" className="font-mono text-xs" spellCheck={false} autoComplete="off" value={selection.endpoint} placeholder={preset.placeholder} onChange={(e) => onSelect({ ...selection, endpoint: e.target.value })} aria-invalid={selection.endpoint.trim() !== "" && !endpointOrigin(selection.endpoint)} />
            <p className="text-xs text-muted-foreground">The server's origin (<code>http(s)://host:port</code>) or its full request URL; the path can be changed on the Model step. A <code>http://</code> origin is saved with <code>allowInsecure</code>.</p>
            {selection.endpoint.trim() !== "" && !endpointOrigin(selection.endpoint) && <p role="alert" className="text-xs text-red-400" data-testid="celln-endpoint-error">Enter an http:// or https:// URL.</p>}
          </div>
          {preset.kind === "custom" && (
            <div className="space-y-1.5">
              <Label>Protocol</Label>
              <Select value={selection.protocol} onValueChange={(value) => onSelect({ ...selection, protocol: value as CellnProtocol })}>
                <SelectTrigger className="h-8 w-64 text-xs" data-testid="celln-protocol"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="openai-chat">OpenAI-compatible (chat completions)</SelectItem>
                  <SelectItem value="anthropic-messages">Anthropic-compatible (messages)</SelectItem>
                </SelectContent>
              </Select>
            </div>
          )}
          <label className="flex items-center gap-2 text-xs">
            <input type="checkbox" data-testid="celln-keyed" checked={selection.keyed} onChange={(e) => onSelect({ ...selection, keyed: e.target.checked })} />
            This endpoint needs an API key
          </label>
        </div>
      )}
      {route && origin && <OriginVerdict origin={origin} mediation={mediation} />}
      {route && origin && originStatus(origin, mediation) === "allowed" && (
        <p className="break-words text-xs text-muted-foreground" data-testid="celln-route-summary">
          {route.protocol} on <code>{origin}</code>, any model{route.policy ? <>; policy <code>{route.policy}</code> admits this origin</> : ""}. {route.auth === "none" ? "No key is required. This Agent gets its own model connection." : "This Agent gets its own key and its own model connection; nothing is shared with another Agent, and the key stays pinned to this origin."}
        </p>
      )}
      <p className="text-xs text-muted-foreground">Any OpenAI- or Anthropic-compatible endpoint works; the namespace's policy only decides which origins an Agent's key may travel to (<a className="underline" href={GUIDE_URL} target="_blank" rel="noreferrer">guide</a>).</p>
    </div>
  );
}

/**
 * The Auth step: paste a key (the API writes it into a Secret in this
 * namespace under the protocol's fixed key name) or link a Secret that already
 * holds it. The pasted key lives in this component's parent state only until
 * it is submitted; it is never stored, logged or shown back.
 */
export function CellnKeyStep({ route, value, onChange, managedSecretName }: {
  route: CellnMediatedRoute;
  value: KeyChoice;
  onChange: (choice: KeyChoice) => void;
  /** The Secret a pasted key is written to. */
  managedSecretName: string;
}) {
  const namespace = getNamespace();
  const secrets = useCellnKeySecrets(route.secretKey, route.auth !== "none" && value.mode === "existing");
  const [copied, setCopied] = useState(false);
  if (route.auth === "none") return <p data-testid="celln-keyless-route" className="text-sm text-muted-foreground">No API key is required for this model endpoint. The gateway sends requests without credentials to the route approved by your operator.</p>;
  const command = `kubectl -n ${namespace} create secret generic <name> --from-literal=${route.secretKey}=<your key>`;
  const tab = (mode: KeyChoice["mode"], label: string) => (
    <button
      type="button"
      data-testid={`celln-key-mode-${mode}`}
      aria-pressed={value.mode === mode}
      onClick={() => onChange(mode === "create" ? { mode, apiKey: "" } : { mode, secretName: "" })}
      className={cn("flex-1 rounded-md border px-3 py-2 text-xs transition-colors", value.mode === mode ? "border-blue-500/40 bg-blue-500/15 text-blue-300" : "border-border/50 hover:bg-white/5")}
    >
      {label}
    </button>
  );
  return (
    <div className="space-y-4" data-testid="celln-key-step">
      <div className="flex gap-2">{tab("create", "Create a new key")}{tab("existing", "Use an existing Secret")}</div>
      {value.mode === "create" ? (
        <div className="space-y-2">
          <Label htmlFor="celln-api-key">{route.provider} API key</Label>
          <Input id="celln-api-key" data-testid="celln-api-key" type="password" autoComplete="off" spellCheck={false} value={value.apiKey} onChange={(e) => onChange({ mode: "create", apiKey: e.target.value })} placeholder="Paste the key" />
          <p className="break-words text-xs text-muted-foreground">
            Written once to Secret <code>{managedSecretName}</code> in namespace <code>{namespace}</code> under the key <code>{route.secretKey}</code>, which the {route.protocol} protocol fixes. Only the model gateway reads it; the console never shows it again.
          </p>
        </div>
      ) : (
        <div className="space-y-2">
          <div className="flex items-center justify-between gap-2">
            <Label>Secret in {namespace}</Label>
            <Button type="button" size="sm" variant="ghost" className="h-7 gap-1 text-xs" data-testid="celln-key-refresh" onClick={() => secrets.refetch()} disabled={secrets.isFetching}>
              <RefreshCw className={cn("h-3 w-3", secrets.isFetching && "animate-spin")} /> Refresh
            </Button>
          </div>
          {secrets.isError ? (
            <p role="alert" className="break-words text-xs text-red-400">Could not list Secrets: {secrets.error instanceof Error ? secrets.error.message : "request failed"}</p>
          ) : secrets.isLoading ? (
            <p className="flex items-center gap-2 text-xs text-muted-foreground"><Loader2 className="h-3.5 w-3.5 animate-spin" /> Looking for Secrets holding {route.secretKey}…</p>
          ) : (secrets.data || []).length === 0 ? (
            <p role="status" className="text-xs text-amber-500" data-testid="celln-key-none">No Secret in <code>{namespace}</code> holds <code>{route.secretKey}</code> yet. Create one with the command below, then refresh.</p>
          ) : (
            <ScrollArea className="max-h-36 rounded-md border border-border/50">
              <div className="space-y-0.5 p-1" data-testid="celln-key-secrets">
                {(secrets.data || []).map((secret) => (
                  <button
                    key={secret.name}
                    type="button"
                    data-secret={secret.name}
                    onClick={() => onChange({ mode: "existing", secretName: secret.name })}
                    className={cn("flex w-full items-center gap-2 rounded-md border px-2.5 py-1.5 text-left font-mono text-xs transition-colors", secret.name === value.secretName ? "border-blue-500/30 bg-blue-500/15 text-blue-400" : "border-transparent text-foreground hover:bg-white/5")}
                  >
                    {secret.name === value.secretName && <Check className="h-3 w-3 shrink-0" />}
                    <span className="truncate">{secret.name}</span>
                    {secret.managed && <span className="ml-auto shrink-0 font-sans text-[10px] text-muted-foreground">created by the console</span>}
                  </button>
                ))}
              </div>
            </ScrollArea>
          )}
          <p className="break-words text-xs text-muted-foreground">
            The Secret must be in namespace <code>{namespace}</code> and hold the key under exactly <code>{route.secretKey}</code> (the {route.protocol} protocol fixes the name, whatever the provider is called). Only Secrets that already do are listed; their values are never read here. To create one yourself:
          </p>
          <div className="flex items-start gap-2">
            <pre className="min-w-0 flex-1 overflow-x-auto whitespace-pre-wrap break-all rounded bg-muted/50 p-2 font-mono text-[11px]" data-testid="celln-key-command">{command}</pre>
            <Button type="button" size="sm" variant="outline" className="h-7 shrink-0 gap-1 text-xs" onClick={() => { navigator.clipboard?.writeText(command).then(() => { setCopied(true); setTimeout(() => setCopied(false), 2000); }).catch(() => undefined); }}>
              {copied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />} {copied ? "Copied" : "Copy"}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

/**
 * The Model step: the model name on the chosen provider, its endpoint origin
 * (fixed on the Provider step; never free text here) and request path, and
 * this Agent's own model parameters and output-token limit. A route with an
 * exact model list (an existing connection) is a choice instead.
 */
export function CellnModelStep({ route, model, onModel, origin, onOrigin, path, onPath, parameters, onParameters, maxOutputTokens, onMaxOutputTokens }: {
  route: CellnMediatedRoute;
  model: string;
  onModel: (model: string) => void;
  origin: string;
  /** Offered only for a route with several origins; the Provider step fixes a new Agent's. */
  onOrigin?: (origin: string) => void;
  path: string;
  onPath: (path: string) => void;
  parameters: string;
  onParameters: (text: string) => void;
  maxOutputTokens: string;
  onMaxOutputTokens: (text: string) => void;
}) {
  return (
    <div className="max-h-[60vh] space-y-4 overflow-y-auto pr-1" data-testid="celln-model-step">
      {route.anyModel ? (
        <div className="space-y-2">
          <Label htmlFor="celln-model-name">Model</Label>
          <Input id="celln-model-name" data-testid="celln-model-name" className="font-mono text-xs" spellCheck={false} autoComplete="off" maxLength={128} value={model} onChange={(e) => onModel(e.target.value)} placeholder="Model name, e.g. gpt-4o" aria-invalid={model !== "" && !validModelName(model)} />
          {model !== "" && !validModelName(model) ? (
            <p role="alert" className="text-xs text-red-400" data-testid="celln-model-name-error">A model name is 1–128 bytes with no spaces or control characters, and not “*” itself.</p>
          ) : (
            <p className="text-xs text-muted-foreground">Type the {route.provider} model name this Agent uses; the connection lists exactly it. The request goes only to the origin below.</p>
          )}
        </div>
      ) : (
      <div className="space-y-2">
        <Label>Model</Label>
        <div className="space-y-0.5 rounded-md border border-border/50 p-1" data-testid="celln-models">
          {route.models.map((candidate) => (
            <button
              key={candidate}
              type="button"
              data-model={candidate}
              aria-pressed={candidate === model}
              onClick={() => onModel(candidate)}
              className={cn("flex w-full items-center gap-2 rounded-md border px-2.5 py-1.5 text-left font-mono text-xs transition-colors", candidate === model ? "border-blue-500/30 bg-blue-500/15 text-blue-400" : "border-transparent text-foreground hover:bg-white/5")}
            >
              {candidate === model && <Check className="h-3 w-3 shrink-0" />}
              <span className="truncate">{candidate}</span>
            </button>
          ))}
        </div>
        <p className="text-xs text-muted-foreground">The models this connection lists for {route.provider}. A run on any other model is refused, so there is no free-text model here.</p>
      </div>
      )}
      <div className="space-y-2">
        <Label>Endpoint</Label>
        <div className="flex flex-wrap items-center gap-2">
          {route.endpointOrigins.length > 1 && onOrigin ? (
            <Select value={origin} onValueChange={onOrigin}>
              <SelectTrigger className="w-auto min-w-[14rem] font-mono text-xs" data-testid="celln-origin-select"><SelectValue placeholder="Choose an origin" /></SelectTrigger>
              <SelectContent>{route.endpointOrigins.map((candidate) => <SelectItem key={candidate} value={candidate}>{candidate}</SelectItem>)}</SelectContent>
            </Select>
          ) : (
            <code className="rounded bg-muted/50 px-2 py-1.5 text-xs" data-testid="celln-origin">{origin}</code>
          )}
          <Input aria-label="Request path" data-testid="celln-endpoint-path" className="h-8 w-56 font-mono text-xs" spellCheck={false} value={path} onChange={(e) => onPath(e.target.value)} />
        </div>
        <p className="text-xs text-muted-foreground">The origin is the one chosen on the Provider step (the policy admitted it); the request path defaults to the {route.protocol} standard and may differ on a gateway.</p>
      </div>
      <CellnModelParametersField value={parameters} onChange={onParameters} showThinking={offersThinkingSwitch(route)} maxOutputTokens={maxOutputTokens} onMaxOutputTokensChange={onMaxOutputTokens} />
    </div>
  );
}

/** What exists and what does not, after a save that did not finish. */
export function OwnKeyProgress({ steps, error }: { steps: OwnKeyStep[]; error: string }) {
  const label: Record<OwnKeyStep["state"], string> = { done: "done", failed: "failed", "not-started": "not created" };
  return (
    <div role="alert" className="space-y-2 rounded-lg border border-red-500/30 bg-red-500/5 p-3 text-xs" data-testid="celln-own-key-progress">
      <p className="break-words font-medium text-red-400">{error}</p>
      <ul className="space-y-1">
        {steps.map((step) => (
          <li key={step.step} data-step={step.step} data-state={step.state} className="flex flex-wrap items-baseline gap-x-2">
            <span className={cn("w-20 shrink-0 font-medium", step.state === "done" ? "text-emerald-400" : step.state === "failed" ? "text-red-400" : "text-muted-foreground")}>{label[step.state]}</span>
            <code className="break-all">{step.object}</code>
            {step.note && <span className="text-muted-foreground">({step.note})</span>}
          </li>
        ))}
      </ul>
      <p className="text-muted-foreground">Every step is safe to repeat: fix the cause and press Create again, and what is already done is kept. Nothing above is removed automatically.</p>
    </div>
  );
}
