# Review of "Celln: Agent-bound model access"

Date: 2026-10-22. Four independent reviews (architect, engineer, assessor,
removal inventory) of `celln-agent-bound-model-access.md`, grounded in the
Sympozium code and Celln `origin/main` (39e3419). This file records where they
agree, where they contradict the design's decisions, and the revised plan.

## Verdict

The core move is right and the code already supports it. Since #650 no Celln
node holds a key; the gateway re-checks the connection digest, Secret UID,
Agent UID and key owner on every call (`internal/modelgateway/gateway.go:300-341`).
The operator route list is the only thing left that decides *which* provider a
tenant's own key may reach (`internal/cellnauthority/platform_resolver.go:606-622`).
Removing backends, per-backend profiles, routes, `apply-routes`, the starter
Agent and the mediation switch loses none of the properties in the design's
"what we keep" table. Roughly 14,000 lines go (see the inventory).

Three parts of the design do not hold as written. All four reviewers found the
same three independently.

## Where the reviews contradict the design's decisions

### 1. Pi and Hermes in a cell cannot ship in 0.12 (decision 1)

The design describes "three small pieces". The code says otherwise:

- The only host↔guest data path is a port-I/O mailbox: **one exchange at a
  time**, frames capped at 61,440 bytes with a hard `< 0xffff` invariant, and
  commit *yields the vCPU to the host* (`celln-warden/src/parent_mailbox.rs:9-23`).
  A chat body with history plus an SSE answer needs a multi-frame streaming
  protocol, and a Pi/Hermes server needs an egress model call in flight while
  the ingress frame is still open. The single-exchange state machine forbids
  both. This is a transport redesign, not a channel.
- seccomp cannot inspect socket addresses, so "loopback only" is not
  enforceable by the filter; the claim rests entirely on the missing NIC, and
  pilot must bring `lo` up (`celln-pilot/src/guest.rs:413-430`).
- The broker's chat validator is `deny_unknown_fields`, text-only, 32 KiB
  (`celln-warden/src/egress_post.rs:131-245`); the gateway refuses `stream`
  and requires `max_tokens` (`validate.go:205,235`). A shim must inject and
  strip fields per adapter.
- 256 MiB cells, CRD cap at 256 MiB (`agentruntime_celln_types.go:45`),
  python/node rootfs as signed ≤ 512 MiB closures untested.
- Payoff is near zero today: Pi runs `--no-tools` and Hermes disables its
  toolsets, so a harness cell is the model plus a 1 GiB VM, which the native
  harness already gives in 256 MiB.

Engineer's size: XL, larger than slices 2–5 combined, months of Celln work.
**Recommendation: its own design doc; 0.12 ships native-only.** Goal 3 ("deploy
a model harness, pick a provider, it works with Celln") is met for the native
harness; Pi/Hermes stay on the Kubernetes plane with the same ModelConnection.

### 2. "Private LAN allowed, cluster ranges refused" is not implementable (decision 2)

The gateway's dialer sees only an IP (`transport.go:44-48`, `forbiddenIP`
`validate.go:284-291`). Pod and service CIDRs are RFC 1918 and are not
discoverable portably; the API server on managed clusters is often a public IP.
Allowing all private space while "always refusing cluster ranges" needs CIDRs
the gateway does not have. Keyless connections make it worse: keyless +
private-by-default is an authenticated port scanner for any tenant, and the
uniform 502 still leaks open/closed by timing.

**Recommendation: keep today's exact-origin allow-list for private
destinations** (`modelGateway.privateOrigins`), but make it a one-step
operator approval surfaced in the console ("connection to a private origin:
pending operator approval"), and allow `allowPrivate` only together with an
explicit `clusterCIDRs` value. Public HTTPS stays open. Also handle NAT64
`64:ff9b::/96` and read the `kubernetes` Endpoints addresses at startup.

### 3. Privileged DaemonSets must not be on by default

`celln-fleet.yaml:334,646` are `privileged: true` with hostPath state,
`/dev/kvm`, `/boot` and `/lib/modules`. The chart's own stance is that
privileged host mutation is "a separate, deliberate opt-in" (`values.yaml:543-547`),
and PSA/Kyverno in most multi-team clusters will reject them.

**Recommendation:** `celln.enabled: true` renders controller wiring, gateway,
trust and the KVM probe. The fleet DaemonSets render too, but
`nodeProbe.labelKVMNodes` defaults **false** under plain `helm install`, so no
privileged pod lands until an operator labels a node. `sympozium install`
(interactive, consenting) turns auto-labelling on after printing one line
("Celln fleet: privileged DaemonSet on N KVM nodes; `--no-celln` to skip").
Goal 1 holds: nothing about models is asked either way. The legacy
`celln-installer` DaemonSet (hostPID + host root, node-held provider keys at
`celln.yaml:184-195`) is deleted in 0.12; it contradicts "no key on a node".

## Findings that refine the design (no contradiction)

**R1 (blocker) Origin pin: required, not TOFU.** Anyone with Secret write can
rewrite the annotation, so TOFU buys nothing over "refuse an unpinned Secret"
and adds a race (an actor with ModelConnection write but not Secret write wins
the first claim with their origin). API and console always write
`sympozium.ai/model-key-origin` with `model-key-owner` in the same Update;
`kubectl` users add one line; the resolver refuses keyed connections whose
Secret lacks it. Enforce at `PinCredentialSource` (`gateway.go:62-85`, before
any VM boots), at `liveRoute` (`gateway.go:321-339`, per call), and in the API
update path (`server_modelconnections.go:100-115`), which must refuse an origin
change on a pinned Secret or the console is the rewrite vector. Pin
`scheme://host[:port]` only. The validation webhook can additionally reject an
annotation change without a key-bytes change, but per CLAUDE.md nothing may
depend on admission. Say plainly in the doc: the pin is namespace-trust, the
same strength as `model-key-owner`; it does not restore "no namespace actor
can pick the destination".

**R2 (should-fix) One-line operator guardrail.** Replace the "narrow (opt-in)"
sentence with concrete `allowedOrigins` / `deniedOrigins` host globs on the
policy, default empty, evaluated in `validateConnection`. Operators get
`allowedOrigins: ["*.openai.com", "llm.corp.internal"]` without declaring
protocol/model/provider tuples. Goals 2 and 5 survive.

**R3 (should-fix) "One runtime per scope" → one profile per (scope, harness
kind).** `validateRuntimeProfile` hard-wires contract, lane, `maxTurns ≤ 6`,
`workspace == none` (`platform_resolver.go:885-891`); a future harness cell is
a different closure and profile. Model: `celln-<scope>` (+`.toolbox`) now,
`celln-<scope>-<harness>` later. Keep `celln-native`/`celln-<backend>` wrappers
as rebound aliases, or every Agent's `runtimeRef` breaks. Make the wrapper
controller-defaulted and stop exposing it in the API.

**R4 (should-fix) Trust hook mechanics.** `PrepareMediationTrust` needs both
namespaces to exist (`mediation.go:124-129`), `celln-system` is a chart
resource, and controller/gateway/node pods mount the trust non-optionally, so
with `--wait` a `post-install` hook never runs. Fix: optional mounts with
readiness gating, a bootstrap subcommand in the controller image (its
entrypoint is `/controller` only), RBAC scoped to named Secrets/ConfigMaps in
two namespaces, runtime trust reload in gateway and controller (no `keyId`
value → read `kid` from JWKS), a `trust rotate` command using the multi-key
JWKS the verifiers already accept, `doctor` detects absent trust, and a
documented `kubectl create job --from=` fallback for Argo with hooks off.
Partial pre-existing trust must not fail every `helm upgrade`.

**R5 (should-fix) Upgrade from 0.11.x.** Every enduring run's decision pins
the runtime digest including the profile spec (`platform_resolver.go:482-487`),
so **all live Celln conversations end at upgrade**. Say so. Fleet-keyed
objects (`celln-agent-<b>`, `celln-<b>` connections, credentials Secret,
backends-extra, per-backend profiles) need an explicit 0.12 cleanup path;
own-key Agents keep working and get pinned on first run only if `decide`
returns the Secret when unowned *or* unpinned (`modelkey.go:131-140`). Old
values with `celln.fleet.backends` must `fail` with migration text.

**R6 (should-fix) Multi-team fairness.** One global semaphore
(`types.go:166`), budgets per run/turn only (`modelbudget/store.go:154`). Add a
per-namespace concurrency share and a namespace spend ceiling
(`namespace_uid` is already on every row). Gateway refusals get a reason-coded
event on the run.

**R7 (should-fix) Server-side clamp.** Connections are now tenant-authored;
clamp `maxOutputTokens` to `ceiling/turnModelRequests` in the API
(`platform_resolver.go:747-755`) or the first custom connection hits
`AUTH_LIMIT_OUT_OF_RANGE`.

**R8 (should-fix) Agent-owned children.** The one-call API should set
`ownerReferences` on the Secret and ModelConnection so delete is one operation
and `LiveOwner` staleness never arises.

**R9 (consider) OpenAI-compatible endpoint semantics.** "Last user message
into the enduring conversation" diverges from every client, which resends full
history, and concurrent clients share one single-exchange parent. Make it
stateless (one-shot run, history in the task) or require a conversation id.
Defer to after `/messages`.

**R10 (confirmed) `modelkey.Grants` gap.** Reads `authRefs` only
(`modelkey.go:79-86`) while the resolver honours `execution.modelConnectionRef`
(`platform_resolver.go:653-663`). Fix: one predicate `GrantsConnection(agent,
conn)` used by both, with a test asserting they agree. Slice 2.

**R11 (hidden coupling)** Slice 1's `configured.json` shape change breaks
`readBackendConfiguration` (`cellninstall/platform.go:293-300`) until slice 2;
the Sympozium reader must accept both shapes before the Celln release is
pinned. `requestOutputTokens` already comes from operator material, not the
decision; the Celln change in slice 1 is only `starter-configure`.

**R12 (decision needed)** `internal/cellnparent/platform.go` (~930 LOC) exists
only for the host-profile provision path (`:196` requires `Auth ==
"host-profile"`). With fleet keys gone it has no model-using caller. Drop it,
or keep a model-free variant.

## Revised plan

Slices, one PR each, unit tests per seam, one journey at the end. Breaking,
0.12.

1. **Celln model-free** `starter-configure`; Sympozium reader accepts both
   `configured.json` shapes; single configuration dir name. Release + pin.
2. **Core**: connection is the route; required origin pin written at create
   and at claim, enforced at Pin/liveRoute/API update; `GrantsConnection`;
   named Secret key; HTTPS port allowed; `allowedOrigins`/`deniedOrigins`
   globs; `maxOutputTokens` clamp; one profile per scope with aliased wrappers;
   private destinations unchanged (`privateOrigins`, now surfaced as pending
   approval).
3. **Install**: `celln.enabled: true` with gateway, trust bootstrap Job and
   probe; fleet DaemonSets render but auto-label off under plain Helm, on from
   `sympozium install`; legacy `celln-installer` deleted; backends, routes,
   starter Agent, mediation switch and their flags removed; `doctor` updated;
   upgrade path with explicit "live conversations end" notice and fleet-keyed
   cleanup.
4. **API + console**: one-call `POST /agents` with Agent-owned children;
   `POST /agents/{name}/messages`; wizard presets + Custom; per-namespace
   gateway share and spend ceiling; refusal events.
5. **Docs + journey**: rewrite both guides; two teams, three providers (keyed
   canary, custom OpenAI-compatible, keyless local via approved private
   origin), refused key reuse, refused origin change, unpinned Secret refused,
   API-only run.
6. **Later, own docs**: Pi/Hermes in a cell (mailbox transport redesign);
   OpenAI-compatible per-Agent endpoint.

## Already dead, independent of this design

From the inventory: the one-shot router / single dispatcher plane with
node-held provider keys (~1,500 LOC), the `--celln-native` single managed plane
(~1,450), eight unreferenced functions (~100), `celln.openaiBaseUrl`. The
legacy namespaced issuer/review stack (`internal/cellnreview`, `CellnTool`,
inline `spec.celln`, `cmd/celln-review-ui`, ~10,000 LOC) is wired only for
"execution recovery" and is a separate decision. See
`celln-removal-inventory.md`.
