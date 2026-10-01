# Persistent Workspaces

By default an agent pod's `/workspace` is an ephemeral `emptyDir`: it is gone
when the run's pod is. A **persistent workspace** keeps `/workspace` across
runs of the same conversation, backed by a PVC per `(Agent, sessionKey)`
pair. Harnesses that keep session state on disk (Codex, Claude Code, …) need
it, and it helps any agent that should pick up where the previous turn of a
conversation — a long-lived Slack thread, say — left off.

Each persistent workspace is a `WorkspaceSession` (short name `ws`). This
applies to the Kubernetes (`job`) backend; Celln runs keep their files in the
parent cell instead (see [Celln Backend](celln-backend.md)).

## Enabling it

Opt in on the Agent:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: coder
spec:
  agents:
    default:
      model: gpt-4o
  workspace:
    perSessionPVC: true
    size: 5Gi              # default 1Gi
    storageClassName: ""   # cluster default when empty
    idleTTL: 168h          # default 720h (30 days); "0s" disables reclamation
```

Every AgentRun of that Agent with a `spec.sessionKey` then gets the
session's workspace mounted at `/workspace`. Runs from channels, schedules and
the API carry session keys that stay stable for a conversation, so their
turns share one workspace.

## How it behaves

- **One PVC per session.** The controller creates the `WorkspaceSession` on
  the first run of a session; it owns a PVC named
  `ws-<agent>-<hash>-g<generation>`. Deleting the session, or its Agent,
  deletes the PVC.
- **One run at a time.** The PVC is `ReadWriteOnce`, so runs of the same
  session are admitted in order: a later run waits with condition
  `Blocked` / `SessionBusy` until its peer finishes. Different sessions run
  in parallel.
- **Idle reclamation.** `status.lastTouchedAt` records the last run; a
  session idle for longer than `idleTTL` is reclaimed with its PVC.
- **Each run leaves a marker.** An init container writes
  `/workspace/.sympozium/state.json` (run, session key, Agent, session and
  PVC names, start time) and keeps the previous run's marker as
  `previousRun`. A marker without `previousRun` means the workspace is fresh,
  so a harness can tell the user that earlier state is gone.
- **Recreation is recorded.** If the PVC has to be recreated (lost, or a
  storage-class change), the generation in its name increments and
  `status.recreatedInfo` records why.

Phases: `Pending` → `Bound`, with `Recreating`, `Releasing` and `Failed`
when those apply.

## Managing workspaces

```bash
kubectl get ws -n team-a                          # Agent, phase and PVC per session
sympozium workspace list -n team-a [--agent coder] [--ensemble my-team]
sympozium workspace show NAME                     # session, PVC and last AgentRun
sympozium workspace exec NAME                     # debug pod with the PVC at /workspace
sympozium workspace delete NAME [--force]         # delete the session and its PVC
```

`workspace exec` starts a short-lived pod (`--image`, default
`alpine:3.20`; `--ttl`, default 1h) and prints the `kubectl exec` command to
attach. It refuses while a live run holds the PVC. `workspace delete` also
refuses while live runs reference the session unless you pass `--force`.

## See also

- [Custom Resources](custom-resources.md#workspacesession)
- [AgentHarness](../guides/agentharness.md) — harnesses that keep session state on disk
- [CLI reference](../reference/cli.md#workspaces)
