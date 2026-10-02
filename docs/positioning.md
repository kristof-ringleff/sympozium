# Positioning: what Sympozium is (and deliberately is not)

Sympozium is a **coordination layer for multi-agent AI systems on Kubernetes**.
That is the whole product. This page exists because AI infrastructure is a
crowded, blurry space, and the fastest way to stay coherent is to write the
boundary down and hold every feature to it.

## The three layers

AI systems on Kubernetes decompose into three layers with three different
jobs. Sympozium is exactly one of them.

| Layer | Owns | Decides |
|-------|------|---------|
| **Sympozium** — coordination | Agents: identity, execution, policy, membrane, ensembles, memory | What agents **do** |
| **[llmfit-dra](https://github.com/sympozium-ai/llmfit-dra)** — capability | Accelerator inventory, fit physics, claims, placement | Where compute **happens** |
| **Serving engines** (vLLM, SGLang, llama.cpp) | Batching, KV cache, disaggregation runtime | How tokens **move** |

## The boundary test

For any proposed feature, ask one question: *does it decide what agents do,
where compute happens, or how tokens move?*

One question, one home. If the answer is not "what agents do", the feature
belongs in another layer — even if Sympozium could technically host it.

## Models are claimed, not placed

Agents need model endpoints. Historically Sympozium grew its own placement
machinery to provide them — node telemetry caches, best-node selection,
hostname pinning. That machinery is being retired: placement moves to
[llmfit-dra](https://github.com/sympozium-ai/llmfit-dra), a Kubernetes DRA
driver that publishes what each accelerator *can do* and lets the **stock
kube-scheduler** place workloads against physics ("this model at 20 tok/s"),
with exclusive allocation and explainable failures.

<p align="center">
  <img src="assets/animations/claim-flow.gif" alt="The model claim lifecycle: llmfit-dra probes accelerators and publishes fit physics; a ModelClaim crosses from the coordination layer to the stock kube-scheduler, which selects the one node that satisfies it; the model is allocated exclusively and the endpoint served back." width="760">
  <br><em>The claim lifecycle: probe → publish → claim → schedule → allocate → serve.
  Sympozium never picks nodes — the claim is the only thing that crosses the boundary.</em>
</p>

The analogy that makes this precise: **a ModelClaim is to llmfit-dra what a
PersistentVolumeClaim is to a CSI driver.** No one calls an application "a
storage product" because its chart contains a `volumeClaimTemplate`. In the
same way, an Ensemble persona declaring

```yaml
modelClaim:
  model: Qwen/Qwen2.5-32B-Instruct
  minTps: 25
```

does not make Sympozium a placement engine. Sympozium governs *whether and
what* an agent may claim (policy, budgets, quotas); the capability layer
decides *whether and where* the claim is satisfiable. Demand and supply,
meeting at the claim.

## What Sympozium is not

- **Not a model-serving platform.** If you want to serve models without
  agents, you don't need Sympozium: use llmfit-dra plus a serving engine
  directly. Sympozium's Model resource exists to give *personas* endpoints,
  not as a general serving product.
- **Not a placement engine.** Sympozium never decides which node or device a
  model runs on. It expresses requirements; the scheduler satisfies them.
- **Not an inference runtime or gateway.** Disaggregated prefill/decode,
  KV-cache transfer, batching strategy — that is the serving layer's job.
  llmfit-dra places the *pools* (its claims can distinguish prefill-grade
  from decode-grade silicon); the engine moves the tokens; a Sympozium
  persona just sees one endpoint.

## Where Celln fits

[Celln](concepts/celln-backend.md) is an **execution backend**, the
counterpart of the Kubernetes Job an agent otherwise runs in: it decides how
an agent's *own* work is isolated (a KVM microVM instead of a Pod), not where
a model is served. It passes the boundary test as "what agents do" — the
agent's lifecycle, tools and authority.

Cell placement follows the same rule as everything else: Sympozium does not
pick the node. The Celln gateway issues each parent on the owner with the
most spare capacity, and a fleet node joins by carrying the
`celln.dev/kvm=true` label. That label is capability inventory, applied today
by node-probe next to its inference discovery, and the same caveat below
applies to it. Accelerator power from ergoz is a read-only view of an
external collector, in the same way as the density dashboard.

## Consequences already scheduled

Two existing subsystems fail the boundary test and are being migrated, not
grown:

- **node-probe** (host inference discovery) is capability inventory — it
  belongs in the supply layer and will move out of Sympozium.
- **The density dashboard** remains as screens, but post-migration it is a
  read-only view of llmfit-dra's published inventory, not a Sympozium
  subsystem.

The full migration map lives in the llmfit-dra repo
([sympozium-integration design](https://github.com/sympozium-ai/llmfit-dra/tree/main/docs/design)).

## The one-liners

- **Sympozium**: a coordination layer for multi-agent AI systems on
  Kubernetes. Agents are Pods or microVMs, policy is CRDs — and when an agent needs a
  model, it *claims* one; Sympozium never decides where it runs.
- **llmfit-dra**: ask for a model instead of a device — capability inventory
  and physics-based placement for heterogeneous accelerators, through the
  stock scheduler.
