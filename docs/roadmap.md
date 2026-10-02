# Sympozium Roadmap

!!! note "Historical document"
    This gap analysis dates from February 2026 and is no longer maintained. Several items have
    shipped since (the web dashboard, `sympozium doctor`, token usage and cost tracking, agent-to-agent delegation
    in Ensembles), and the project's direction has moved to hardware-isolated Celln agents and
    AgentHarness. Current plans live in
    [GitHub issues and epics](https://github.com/sympozium-ai/sympozium/issues) and
    [Discussions](https://github.com/sympozium-ai/sympozium/discussions).

Feature gap analysis against OpenClaw and planned improvements.

Last updated: 2026-02-25

---

## Quick Wins (small effort, high value)

| # | Feature | Description | Status |
|---|---------|-------------|--------|
| 1 | **Usage / token tracking** | Log token counts from LLM responses, surface in AgentRun status | 🔧 In progress |
| 2 | **`sympozium doctor`** | Check CRDs installed, NATS healthy, controller running, webhook registered | Planned |
| 3 | **Chat commands** | `/status`, `/reset`, `/new` in connected channels | Planned |

## Medium-Term

| # | Feature | Description | Status |
|---|---------|-------------|--------|
| 4 | **Webhook triggers** | New CRD or API endpoint that creates AgentRuns on inbound HTTP | Planned |
| 5 | **Model failover** | Fallback provider chain in Agent spec | Planned |
| 6 | **Agent-to-agent comms** | `send_agent_message` tool + cross-instance NATS routing | Planned |
| 7 | **Session compaction** | Summarise memory when context grows too large | Planned |

## Strategic

| # | Feature | Description | Status |
|---|---------|-------------|--------|
| 8 | **Web dashboard** | Read-only view of instances, runs, logs (complements TUI) | Planned |
| 9 | **Skills registry** | `sympozium skills search` + community skill index | Planned |
| 10 | **Browser tool** | Headless Chrome sidecar SkillPack | Planned |

## Channel Expansion

| Channel | Status |
|---------|--------|
| Signal | Planned |
| Google Chat | Planned |
| Microsoft Teams | Planned |
| Matrix | Planned |
| iMessage / BlueBubbles | Planned |
| WebChat | Planned |

## Additional Gaps (lower priority)

| Feature | Notes |
|---------|-------|
| **Group routing** | Mention gating, per-channel activation modes, reply tags |
| **DM pairing security** | Pairing codes for unknown senders before processing messages |
| **Presence / typing indicators** | Real-time typing status in channels |
| **Media pipeline** | Image/audio/video handling, transcription |

## Sympozium's Differentiators (keep investing)

These are areas where Sympozium is ahead due to its Kubernetes-native architecture:

- **CRD-based declarative config** — Agent, AgentRun, SympoziumPolicy, SympoziumSchedule, SkillPack
- **Multi-tenant isolation** — multiple instances with separate RBAC
- **Admission webhook policy enforcement** — SympoziumPolicy + tool gating
- **Ephemeral compute** — agents run as K8s Jobs with auto-cleanup
- **Sidecar injection** — SkillPacks auto-inject sidecars with scoped RBAC
- **NATS JetStream event bus** — scalable async messaging
