# Sympozium

**A coordination layer for multi-agent AI systems on Kubernetes**

Every agent runs in an isolated workload — an ephemeral Pod, or a hardware-isolated
Celln microVM. Every policy is a CRD. Coordinate multi-agent workflows on
Kubernetes — from single tasks and long-running conversations to coordinated teams.
Multi-tenant. Horizontally scalable. Safe by design.

Sympozium decides what agents *do*. Where compute happens belongs to
[llmfit-dra](https://github.com/sympozium-ai/llmfit-dra); how tokens move belongs
to the serving engine. See [Positioning](positioning.md) for the boundary.

<p align="center">
  <img src="assets/demo.gif" alt="Sympozium TUI demo" width="800px">
</p>

---

## Quick Install

=== "Homebrew"

    ```bash
    brew tap sympozium-ai/sympozium
    brew install sympozium
    ```

=== "Shell Installer"

    ```bash
    curl -fsSL https://deploy.sympozium.ai/install.sh | sh
    ```

Then deploy to your cluster and activate your first agents:

```bash
sympozium install          # deploys CRDs, controllers, built-in Ensembles, the Celln plane and ergoz
                           # (with DEEPSEEK_/OPENAI_/ANTHROPIC_API_KEY set: the Celln fleet on every KVM node)
sympozium                  # launch the TUI — go to the Ensembles tab, press Enter to onboard
sympozium serve            # open the web dashboard (port-forwards to the in-cluster UI)
```

!!! tip "New here?"
    See the [Getting Started guide](getting-started.md) — install, deploy, onboard your first agent, and learn the TUI, web UI, and CLI commands.

---

## Why Sympozium?

Sympozium is a **Kubernetes-native platform for orchestrating AI agent teams**. Deploy agents for customer support, code review, data pipelines, incident response, or any domain-specific workflow — each agent gets its own pod, RBAC, and network policy with proper tenant isolation.

Bundle agents into **Ensembles** with delegation, sequential pipelines, and supervision relationships. Give them persistent memory, external tools via MCP servers, and cron schedules — all declared as CRDs and reconciled by controllers.

Every concept that traditional agent frameworks manage in application code, Sympozium expresses as a Kubernetes resource — declarative, reconcilable, observable, and scalable.

---

## Key Features

- **Ephemeral agent pods** — each agent run is an isolated Kubernetes Job with its own security context
- **Hardware-isolated agents (Celln)** — one-shot and long-running agents in KVM microVMs on every labelled node, each Agent choosing its model backend; conversations continue on another node if theirs is lost
- **AgentHarness** — run approved external harnesses (Pi, Hermes) as one-shot runs or persistent chat sessions
- **Skill sidecars** — every skill runs in its own container with auto-provisioned, least-privilege RBAC
- **Ensembles** — pre-configured bundles of agents that activate with a few keypresses
- **Multiple interfaces** — k9s-style TUI, full web dashboard, or CLI
- **Channel integrations** — Telegram, Slack, Discord, WhatsApp
- **Persistent memory** — agents retain context across runs via a SQLite + FTS5 memory server on a PersistentVolume
- **Policy-as-CRD** — feature and tool gating enforced at admission time
- **OpenTelemetry** — built-in observability with traces, metrics, token usage and cost estimates; accelerator power via ergoz
- **Web endpoints** — expose agents as OpenAI-compatible APIs and MCP servers
- **Scheduled tasks** — cron-based recurring agent runs
- **Local inference discovery** — node-probe DaemonSet discovers Ollama/vLLM/llama-cpp on host nodes with automatic model listing and node pinning

---

## Learn More

| Topic | Description |
|-------|-------------|
| [Getting Started](getting-started.md) | Install, deploy, and onboard your first agent |
| [Architecture](architecture.md) | System design and how it all fits together |
| [Custom Resources](concepts/custom-resources.md) | The CRDs that model every agentic concept |
| [Ensembles](concepts/ensembles.md) | Pre-configured agent bundles |
| [Skills & Sidecars](concepts/skills.md) | Isolated tool containers with ephemeral RBAC |
| [Lifecycle Hooks](concepts/lifecycle-hooks.md) | PreRun and postRun containers for setup and teardown |
| [Celln Backend](concepts/celln-backend.md) | Hardware-isolated one-shot and enduring agents |
| [Celln Fleet Installation](guides/celln-fleet-installation.md) | Model backends, toolbox, capacity and operations for the fleet |
| [AgentHarness](guides/agentharness.md) | Approved external harnesses and persistent chat sessions |
| [Security](concepts/security.md) | Defence-in-depth at every layer |
| [Writing Skills](guides/writing-skills.md) | Build your own SkillPacks |
| [Writing Tools](guides/writing-tools.md) | Add new tools to the agent runner |
| [Model Connections](guides/model-connections.md) | Reusable model routes for persistent Hermes and native Celln harnesses |
| [Ollama & Local Inference](guides/ollama.md) | Node-based and in-cluster Ollama setup with auto-discovery |
| [LM Studio](guides/lm-studio.md) | Local GGUF model serving with desktop GUI |
| [llama-server](guides/llama-server.md) | llama.cpp server with full GPU control and node auto-discovery |
| [Unsloth](guides/unsloth.md) | Fine-tuned models served via llama.cpp or vLLM |
| [AWS Bedrock](guides/aws-bedrock.md) | Amazon Bedrock setup with Claude, Nova, and other foundation models |

---

## Project Links

- [GitHub Repository](https://github.com/sympozium-ai/sympozium)
- [Releases](https://github.com/sympozium-ai/sympozium/releases)
- [License](https://github.com/sympozium-ai/sympozium/blob/main/LICENSE) — MIT
