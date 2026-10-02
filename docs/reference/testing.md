# Testing

This document is the quick entry point for running Sympozium tests locally and in CI.

## Local test commands

```bash
# Unit tests with the race detector (the bar for any change)
make test

# go vet, including build-tagged code that `go vet ./...` skips
make vet

# Controller tests against a real API server, no cluster (envtest)
make test-system

# End-to-end tool/channel integration tests (Kind cluster + a model)
make test-integration

# API-first integration regression suite
make integration-tests

# Console UX tests (Cypress) against a running UI
make ux-tests                      # Vite dev server (make web-dev-serve)
make ux-tests-serve                # `sympozium serve` on port 9090
(cd web && npm run test:stubbed)   # specs that stub every API call; no cluster, needs `npx vite`
```

Integration tests work against any OpenAI-compatible provider (a local
`llama-server`, `ollama` or LM Studio as well as OpenAI); see
[Writing Integration Tests](../guides/writing-integration-tests.md) and
[Writing UX Tests](../guides/writing-ux-tests.md).

## What CI runs

Every pull request runs, in `.github/workflows/build.yaml`:

| Job | What it checks |
|-----|----------------|
| **Verify** | `gofmt`, `go vet ./...`, `make vet-tags`, build, `go test -race -short ./...`, generated code up to date, `make helm-sync-check` |
| **System tests (envtest)** | `make test-system` |
| **Console stubbed Cypress specs** | `npm run test:stubbed` (on `web/**` changes) |

Images are built and pushed on `main`.

## Celln journeys

Celln tests need a Linux host with `/dev/kvm`; on Kind, copy the host kernel
into each node first (see
[Celln Fleet Installation](../guides/celln-fleet-installation.md#prerequisites)).

| Script / target | What it proves |
|-----------------|----------------|
| `test/integration/test-celln-fleet.sh` | The fleet on multi-node Kind: backends (including one added from the API), one-shot and enduring runs, the toolbox, tenancy, node loss and continuation |
| `test/integration/test-celln-oneliner.sh` | A bare `sympozium install` brings up the fleet |
| `make test-celln-authorisation-contract` | Namespace-authorisation fixtures (no cluster, no KVM) |
| `make test-celln-model-budget` | Model-budget accounting against a real PostgreSQL (`DATABASE_URL`) |
| `make test-celln-model-gateway-live` | The model gateway against a live API server and PostgreSQL |
| `make test-celln-tenancy-local` | Pinned Go/Rust/database/API/gateway checks and KVM prerequisites |

## API integration suite notes

`make integration-tests` runs the API-focused smoke and behavior checks under `test/integration/`, including:

- API smoke coverage for namespaces/skills/policies/ensembles/instances/schedules
- Ensemble provisioning and provider switch propagation
- Ensemble vs ad-hoc correctness checks
- Schedule dispatch behavior
- AgentRun pod container shape checks
- Observability API checks
- Web-endpoint skill enable/disable/status API checks
- Serving-mode AgentRun shape (Deployment + Service creation)
- Optional capability checks (`CLAUDE_TOKEN`, `GITHUB_TOKEN`)

Optional secrets can be passed locally:

```bash
CLAUDE_TOKEN=... GITHUB_TOKEN=... make integration-tests
```

## Scheduled GitHub Actions workflow

The repository includes a scheduled Kind-based workflow:

- Workflow file: `.github/workflows/integration-kind.yaml`
- Name: `Integration Tests (Kind)`
- Triggers:
  - Daily schedule (`0 6 * * *`, UTC)
  - Manual run via `workflow_dispatch`

### What the workflow does

1. Checks out the repository
2. Sets up Go
3. Creates a Kind cluster
4. Builds Sympozium images (`make docker-build`)
5. Loads images into Kind
6. Installs CRDs and built-ins (`make install`)
7. Applies control-plane manifests
8. Waits for core deployments
9. Runs `make integration-tests`
10. On failure, dumps key cluster diagnostics and logs

### Repository secrets

The workflow passes these optional secrets to tests:

- `CLAUDE_TOKEN`
- `GITHUB_TOKEN`

If unset, the related capability checks are skipped by design.

## Running the workflow manually

In GitHub:

1. Open **Actions**
2. Select **Integration Tests (Kind)**
3. Click **Run workflow**
