import { getNamespace } from "@/lib/api";
import { Label } from "@/components/ui/label";

/**
 * Every Agent is an OpenAI-compatible endpoint at /api/v1/agents/{name}/v1:
 * the newest user message goes to the Agent (a turn of its live conversation,
 * or a new one; a session harness takes the request as is) and the answer
 * comes back as a chat completion. Shown so people can point an SDK at the
 * Agent without reading the API docs.
 */
export function AgentOpenAIEndpoint({ name }: { name: string }) {
  const ns = getNamespace();
  const base = `${window.location.origin}/api/v1/agents/${encodeURIComponent(name)}/v1`;
  const snippet = `from openai import OpenAI
client = OpenAI(base_url="${base}", api_key="<your Sympozium token>",
                default_headers={"X-Sympozium-Namespace": "${ns}"})
reply = client.chat.completions.create(model="${name}",
    messages=[{"role": "user", "content": "hello"}])
print(reply.choices[0].message.content)`;
  return (
    <div className="space-y-2 rounded-md border p-3 text-xs" data-testid="agent-openai-endpoint">
      <Label>OpenAI-compatible endpoint</Label>
      <p className="text-muted-foreground">
        Point any OpenAI SDK at this Agent: base URL <code className="break-all">{base}</code>, your Sympozium token as the API key, and the header <code>X-Sympozium-Namespace: {ns}</code>. The newest user message is the message to the Agent, which holds its own conversation; <code>stream: true</code> is supported.
      </p>
      <pre className="overflow-x-auto rounded bg-muted p-2 font-mono text-[11px] leading-snug">{snippet}</pre>
    </div>
  );
}
