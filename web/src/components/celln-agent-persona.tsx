import { useState } from "react";
import type { Agent } from "@/lib/api";
import { usePatchAgent } from "@/hooks/use-api";
import { MAX_CELLN_SYSTEM_PROMPT_BYTES, systemPromptBytes } from "@/lib/agent-execution";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";

/**
 * The Agent's persona: the system prompt every Celln run of this Agent
 * carries into its cell (execution.systemPrompt). It is agent configuration,
 * saved with the Agent's execution defaults; empty means the fleet's default
 * persona, shown as the placeholder. A cell accepts at most 2048 bytes.
 */
export function CellnAgentPersona({ agent, fleetDefault }: { agent: Agent; fleetDefault?: string }) {
  const patchAgent = usePatchAgent();
  const execution = agent.spec.execution;
  const current = execution?.systemPrompt || "";
  const [draft, setDraft] = useState(current);
  const [editing, setEditing] = useState(false);
  const bytes = systemPromptBytes(draft);
  const tooLong = bytes > MAX_CELLN_SYSTEM_PROMPT_BYTES;

  function save(value: string) {
    const next = { ...(execution || { backend: "celln" as const }) };
    if (value.trim()) next.systemPrompt = value.trim();
    else delete next.systemPrompt;
    patchAgent.mutate({ name: agent.metadata.name, data: { execution: next } }, { onSuccess: () => setEditing(false) });
  }

  return (
    <div className="space-y-2 rounded-md border p-3 text-sm" data-testid="agent-persona">
      <div className="flex items-center justify-between">
        <Label>Persona</Label>
        {!editing && (
          <Button variant="outline" size="sm" onClick={() => { setDraft(current); setEditing(true); }}>
            {current ? "Edit" : "Set persona"}
          </Button>
        )}
      </div>
      {!editing ? (
        current ? (
          <p className="whitespace-pre-wrap text-xs" data-testid="agent-persona-text">{current}</p>
        ) : (
          <p className="text-xs text-muted-foreground">
            This Agent uses the fleet's default persona{fleetDefault ? <>: <span className="italic">{fleetDefault}</span></> : "."}
          </p>
        )
      ) : (
        <div className="space-y-2">
          <Textarea rows={4} value={draft} onChange={(e) => setDraft(e.target.value)} placeholder={fleetDefault || "System prompt this Agent carries into its cell"} />
          <p className={`text-xs ${tooLong ? "text-red-400" : "text-muted-foreground"}`}>
            {bytes}/{MAX_CELLN_SYSTEM_PROMPT_BYTES} bytes. Every new conversation of this Agent carries it into the cell; running conversations keep theirs. Empty restores the fleet's default.
          </p>
          <div className="flex gap-2">
            <Button size="sm" onClick={() => save(draft)} disabled={patchAgent.isPending || tooLong}>Save</Button>
            <Button size="sm" variant="ghost" onClick={() => setEditing(false)} disabled={patchAgent.isPending}>Cancel</Button>
          </div>
          {patchAgent.isError && <p role="alert" className="text-xs text-red-400">{patchAgent.error instanceof Error ? patchAgent.error.message : "Could not save the persona"}</p>}
        </div>
      )}
    </div>
  );
}
