# OpenTelemetry Observability

Sympozium supports OpenTelemetry for agent runs and tool execution. The built-in collector is installed by default with `sympozium install` and enabled by default in the Helm chart.

## Agent-Level Configuration

Enable observability per `Agent`:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: my-agent
spec:
  agents:
    default:
      model: gpt-4o
  observability:
    enabled: true
    otlpEndpoint: sympozium-otel-collector.sympozium-system.svc:4317
    otlpProtocol: grpc
    serviceName: sympozium
    resourceAttributes:
      deployment.environment: production
      k8s.cluster.name: my-cluster
```

## Helm Configuration

The Helm chart deploys a built-in OpenTelemetry collector by default:

```yaml
observability:
  enabled: true
  collector:
    service:
      otlpGrpcPort: 4317
      otlpHttpPort: 4318
      metricsPort: 8889
```

Disable it if you already run a shared collector:

```yaml
observability:
  enabled: false
```

## Web UI Observability Views

<p align="center">
  <img src="../assets/otel.png" alt="Sympozium observability dashboard showing token usage and tool call metrics" width="900px">
</p>

- **Runs page** (`/runs`): collector status, run totals, token totals, tool-invocation totals, model token breakdown
- **Run detail** (`/runs/<name>`) → **Telemetry tab**: run timeline events, trace correlation fields, and observed telemetry metric names

## Trace Propagation

A run is one trace. The controller passes its trace context to the agent pod
as `TRACEPARENT`, so the runner's spans nest under the controller's, and the
runner joins Ensemble delegations and memory-server calls to the same trace.

The runner also sends the W3C `traceparent` header on every LLM request
(OpenAI-compatible and Anthropic providers). An LLM gateway or proxy that
reads it (LiteLLM, Langfuse, an OpenTelemetry-instrumented vLLM, …) can attach
its own spans to the run's trace. The header is injected with a pinned W3C
propagator, so it is sent whenever the run has a trace context, even when the
OTLP exporter is disabled or the collector is unreachable.

## Token Usage and Cost

Every AgentRun records its token usage, and, with `pricing.enabled` (Helm,
default on), an **estimated cost** from the `sympozium-model-pricing`
ConfigMap. Estimates are display-only and never gate a run; local providers
(ollama, llama-server, vLLM, …) are always free. Add or correct prices with
`pricing.extraEntries` rather than editing the ConfigMap, which Helm upgrades
overwrite.

## Accelerator Power

Sympozium reads accelerator power draw from an **energy collector** and
shows it on the density and topology views; `GET /api/v1/power` returns the
current snapshot (`available: false` when there is none).

- `sympozium install` deploys [ergoz](https://github.com/sympozium-ai/ergoz),
  the reference collector, into `ergoz-system` (`--no-ergoz` skips it; a
  failed ergoz install never fails the Sympozium install).
- Discovery is zero-config: any Service labelled
  `sympozium.ai/collector=energy` in an allowed namespace is used, at its
  first port and path `/api/v1/fleet` unless the Service's
  `sympozium.ai/collector-port` / `sympozium.ai/collector-path` annotations
  say otherwise. Anything serving the same fleet-snapshot shape works.
- The allowed namespaces are `energyCollector.discovery.namespaces`
  (default `sympozium-system`, `ergoz-system`). This is a security allowlist —
  a label is cheap, so only list namespaces an administrator controls.
  `energyCollector.enabled: false` turns discovery off.
- Readings above 10 kW per device are rejected as implausible.

## Backend Integration

For full distributed trace waterfall views, configure collector exporters to your preferred backend (Jaeger, Tempo, Datadog, Honeycomb, etc.).
