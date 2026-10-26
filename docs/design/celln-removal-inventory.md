# Removal inventory: Celln agent-bound model access

Produced 2026-10-22 by a code-grounded sweep for the redesign in
`celln-agent-bound-model-access.md`; companion to
`celln-agent-bound-model-access-review.md`. Line numbers are as of branch
`feat/keyed-canary-and-attribution`.

Legend: **D** = dead under the design; **A** = already dead or vestigial today.
LOC are approximate and include tests where named.

## 1. CLI (`cmd/sympozium`)

| Path | What | Why | LOC | Dependents first |
|---|---|---|---|---|
| `celln_fleet_install.go` (728) | backend/model/credential/preflight/starter/mediation fields of `cellnFleetFlags`; `applyBackendFlags`, `parseFleetBackends`, `ensureStarterAgents`, `parseMediatedRoutes`, `mediationValues`, `refuseTwoRouteSources`, `sizeLimits(resolved)`; 24 `--celln-fleet-model-*`/`-backend`/`-skip-preflight`/`-mediated-route`/`-mediate-backends`/`-starter-namespace` flags (64–101) | D | ~500 removed; keep scope/package/https-host/limits/replace-package/wait (~230) | cellninstall rewrite |
| `celln_fleet_defaults.go` (237) | provider prompt, `keyedProviders`, `backendsFromEnvironment` (`SYMPOZIUM_CELLN_BACKEND`), `materializeCredentials`, `enableByDefault` | D | ~200 | |
| `celln_mediation_default.go` (174) | `planMediation`, operator mode, `mediationOffConsequence`, `mediationHosts` | D | ~120 (keep `pinnedModelGatewayImage`/`selectModelGatewayImage`) | trust hook Job |
| `celln_mediation_cmd.go` (81) | `celln-mediation bootstrap`, `apply-routes` | D | 81 | trust hook Job |
| `doctor_mediation.go` (167), `doctor.go:229-249,543+` | route/`mediateBackends`/`ScopedDispatchDisabled`/`checkModelCredentials` checks | D (rewrite; keep trust check) | ~150 | |
| `main.go:1296-1380,1404-1420` | fleet default wiring, `--no-celln-mediation`, `--celln-native-approve-starter-tools`, one-shot-router hint | D | ~80 | |
| `upgrade_cmd.go:155-180` | `celln.mediation.enabled` gate for gateway image refresh | D | ~20 | |
| Tests: `celln_fleet_defaults_test` 101, `celln_fleet_parameters_test` 114, `celln_fleet_max_output_tokens_test` 136, `celln_fleet_mediation_test` 143, `celln_mediation_default_test` 184, `celln_mediation_cmd_test` 99, `doctor_mediation_test` 161 | | D | ~940 | |

## 2. `internal/cellninstall` (7,881 total; ~4,900 goes)

| Path | What | Why | LOC |
|---|---|---|---|
| `fleet.go` (795) | `FleetModel`, `FleetBackend`, `FleetOptions.Model/ModelCredentialFile/Backends`, `CredentialProfileFor`, `Publish*Credential`, `ValidateBackendCredentialFiles`, `PublishedBackends/Names`, `ReadFleetConfigurationFor`, `ConfigurationBackends`, `ModelProviderLlamaServer`, `celln.fleet.backends` in `FleetValues` | D | ~550 (keep trust, KVM, scope/package/limits/httpsHosts) |
| `fleet_extra.go` (281) + test 135 | `celln-fleet-backends-extra`, `AppendExtraBackend`, `RolloutConfigure`, `PublishFleetBackendCredentialValue` | D | 416 |
| `fleet_preflight.go` (148) + test 147 | `PreflightBackend`, `PreflightCredential`, `DetectModel` | D | 295 |
| `mediated_backends.go` (149) | `PlanMediatedBackends`, placeholder publish, `MediatedOnlyBackends` | D | 149 |
| `starter_agent.go` (212) | starter Agent Secret/Connection/Agent, `EnsureStarterRuntimeWrappers`, `ProviderKeyName` | D | 212 |
| `mediation_record.go` (182) + test 221 | `celln-mediated-routes` record, `ApplyMediationRecord`, `MediationValues`, `ValidateMediatedRoutes` | D | 403 |
| `model_parameters.go` (260) + test 312 | per-backend published parameters, change refusals, `strvalsEscape` | D (parameters stay on ModelConnection) | 572 |
| `model_output_tokens.go` (130) + test 287 | `MostExpensiveBackend`, `ResolveFor(backends)`, `HintForBackendModel`, `FewerSessionTurnsWarning`; keep `SessionDefaultsFor`/`ProfileTurnAllowance` | D | ~380 |
| `platform.go` (722) | `MediatedRoute`/`PolicyRoute`/`DefaultMediatedRoutes`/`mediatedPolicyRoutes`, `backendConfiguration`, `PlatformProfileName(scope, backend)`, `PlatformOptions.MediateBackends/MediatedRoutes`, per-backend profile loop, `platformWrappers(..., mediatedOnly)` | D → one profile `celln-<scope>` | ~350 |
| `platform_replace.go:128-179` | `rebindTenantWrappers` per backend / mediatedOnly | D | ~60 |
| `mediation.go` (434) | keep `PrepareMediationTrust` (hook Job); drop `AutoMediationValues`, operator-value `DefaultMediationHosts` | D partial | ~60 |
| Tests: `fleet_test` 444, `mediation_default_test` 373, `platform_mediated_routes_test` 259, `mediation_docs_test` 188, `platform_test` 417 + `platform_toolbox_test` 194 (rewrite), `platform_ceilings/current_package/turn_timeout_test` (~380, rewrite) | | D | ~1,800 net |
| **A**: `ReadFleetConfiguration` (fleet.go:652), `PublishFleetModelCredential` (fleet.go:480), `PublishedModelParameters` (model_parameters.go:102), `Partitioned` (install.go:78), `SortedNames`, `BackendCredentialPath`, `DefaultMediatedRoutes` (duplicated by chart helper; test-only) — 0 non-test refs | A | ~100 |
| **A**: legacy single-backend form `FleetOptions.Model`, `--celln-fleet-model-*`, `celln.fleet.model` (values 693–710; `celln-fleet.yaml:35-38`) | A (superseded by `backends`) | folded above |

## 3. `internal/cellnplatform` (437 + 242 test)

`BackendLabel`, `DefaultBackend`, `MediationOnlyLabel`, `WrapperNames(backend)`,
`Toolbox/ToolboxProfileName`, `TenantWrappers` (writes a host-profile
`ModelConnection`), `ProfileRoute(..., "host-profile")`, `EnsureWrappers`,
`RuntimeWrapper` labels: D, ~200 LOC → single `celln` wrapper per namespace
(aliases kept for migration). **A**: `ProfileTools` (line 314), 0 references.

## 4. API server (`internal/apiserver`)

| Path | What | Why | LOC |
|---|---|---|---|
| `celln_backends.go` 436, `celln_backends_test.go` 262, `celln_backends_mediation_test.go` 200 | `GET/POST /celln-platform/backends`, `completeCellnFleetBackend`, `planAddedBackendKey`; `server.go:81,204,206` | D | 898 |
| `celln_mediation.go` 116 + test 170 | `GET /celln-platform/mediation` routes/pending | D | 286 |
| `server.go:530-560,617-630` | profile fields `CredentialProfile`, `MediationOnly`, `StarterAgent/Namespace` | D | ~40 |
| `server_modelconnections.go:57`, `server.go:1310`, `celln_platform_test.go` 205 | credentialProfile refusal; Celln+harness refusal (stays until harness-in-cell); test rewrite | D | ~120 |

## 5. Controller, authority, parent, CRD types

| Path | What | Why | LOC |
|---|---|---|---|
| `internal/controller/celln_scoped.go:44-160,203,241,255` | `routeAuthHostProfile`, `selectedRouteAuth`, `platformServesOneShot`, host-profile→provision vs secret→scoped split, `ScopedDispatchDisabled` hold | D (rewrite) | ~150 |
| `celln_route_path_test.go` 263, `celln_scoped_test.go` (part) | host-profile path tests | D | ~300 |
| `internal/cellnauthority/platform_resolver.go:551-650,672-700` | `policy.Spec.Routes` loop, host-profile auth, protocol-fixed `secretKey` (640–645) → origin pin + named key + origin globs | D (rewrite) | ~80 |
| `platform_host_profile_test.go` 207, `platform_secret_route_test.go` 451, `platform_resolver_test.go` (part) | | D | ~500 |
| `internal/cellnparent/platform.go` 347 + test 387 (+ `provision_local.go` 200) | `PlatformProvisioner`/`BuildPlatformProvisionPlan`; line 196 requires `Route.Auth == "host-profile"`; `CELLN_PARENT_REGISTRATIONS` wiring in `cmd/controller/main.go:229-236` | D (**decision**: drop, or keep model-free) | ~930 |
| `api/v1alpha1/celln_platform_types.go:178-235,271` | `CellnExecutionPolicyRoute` + CEL, `Spec.Routes` → `allowedOrigins`/`deniedOrigins` | D | ~60 + CRD regen (3 copies) |
| `celln_platform_types.go:93-96`, `modelconnection_types.go:34-37,80-107`, `agentrun_types.go:231-233` | `CredentialProfile` fields and validations | D | ~30 |
| `internal/modelconnection/resolve.go:62-70,122`, `internal/controller/celln_harness.go:12`, `internal/cellnauthority/model.go` | credentialProfile branches; `ModelPolicyDocument` (legacy issuer only) | D / A | ~40 / 152 |
| `test/system/celln_starter_agent_test.go` 58, `celln_any_model_route_test.go` 45, `celln_keyless_route_test.go` 43 | | D | 146 |
| `internal/cellncapability/types.go:39`, `cmd/celln-authorisation-fixture/model_types.go:285` | `AUTH_ROUTE_MISMATCH`: keep for model-free/mismatch/ungranted/revision cases; drop route-list wording | update | |

## 6. Model gateway

`types.go:142` `AllowPrivateOrigins` stays (private approvals are kept per the
review); `validate.go:109-112` protocol-fixed secret key → named key;
`gateway.go:321-339` gains the origin-pin check; new `allowedOrigins`/
`deniedOrigins` evaluation in `validateConnection`; `README.md` section (~40);
`test/integration/celln-scoped-real/main.go:188`,
`hack/generate-celln-framework-review.sh:136`. ~50 LOC changed + ~80 test edits.

## 7. Helm chart

| Path | What | Why | LOC |
|---|---|---|---|
| `values.yaml:657-724` | `celln.fleet.backends`, `modelCredentialsSecret`, `model`, `backendsExtraRevision` | D | ~70 |
| `values.yaml:770-833` | `celln.mediation.enabled`, `mediateBackends`, `defaultRoutes`, `routes`, `issuer.keyId` (trust Secret names stay) | D | ~60 |
| `values.yaml` `modelGateway.enabled`/`configurationClaim` standalone mode | D | ~15 |
| `templates/celln-fleet.yaml:22-130,138,274-277,367-391,654-697` | backend loop/validation, `FLEET_BACKENDS`, first-backend `parentRequestDefault`, `backends-extra` annotation, `model-credentials` mounts | D | ~200 of 717 |
| `templates/celln-mediation.yaml:26-55` | `celln-mediated-routes` ConfigMap | D | ~30 |
| `templates/_helpers.tpl:160-260` | `sympozium.cellnMediation` route validation, `cellnDefaultMediatedRoutes` | D | ~100 |
| `templates/model-gateway.yaml:1-33` | mediation gating | D | ~20 |
| `files/celln/fleet-prepare.sh:17,92-135`, `fleet-plan.py:34-63` | backends-extra, credential wait, `modelConnection`/`credentialFile` in plan | D (model-free plan) | ~110 |
| `templates/celln.yaml` (680) | legacy one-shot router / installer DaemonSet (hostPID, host root, node-held provider keys 184–195) | **A**, delete in 0.12 | 680 |
| Chart tests: `celln_mediation_test.go` 763, `celln_fleet_test.go` 378, `celln_fleet_max_output_tokens_test.go` 176, `celln_fleet_plan_test.go` 200, `celln_fleet_publish_test.go` 156 (part), `celln_single_plane_test.go` 99, `celln_dispatcher_test.go` 76 | | D/A | ~1,600 net |
| **A**: `celln.openaiBaseUrl` (values:882), read by no template | A | 2 |

## 8. Web (`web/src`, `web/cypress`)

| Path | What | Why | LOC |
|---|---|---|---|
| `components/celln-fleet-setup.tsx` 91, `lib/celln-fleet-setup.ts` 53, `cypress/e2e/celln-fleet-setup.cy.ts` 57 (**untracked**, this branch) | "no fleet" install-command panel | D | 201 |
| `components/celln-own-key.tsx:54-105` `CellnRouteStep`; route-shaped props on `CellnKeyStep/CellnModelStep` | → presets + Custom | D | ~150 rewrite |
| `lib/celln-own-key.ts` (`routeId`, `initialModelFor`, `routeForConnection`, `profileForRoute`, `GUIDE_URL`), `lib/celln-routes.ts` 25 | route matching | D | ~105 |
| `lib/api.ts:197-226,1670`, `hooks/use-api.ts:89-92` | `CellnMediatedRoute`, `CellnMediation`, `useCellnMediation`; profile `mediationOnly/starterAgent` | D | ~45 |
| `components/onboarding-wizard.tsx:676-740,829-860,1217-1260,1521,1641,2096-2111,2256` | route state, fleet-watch, starter-agent note, "one-shot router alone" hint | D | ~150 rewrite |
| `components/celln-agent-connection.tsx:25,58,136-140` | "no declared route matches" | D | ~20 |
| `lib/run-diagnosis.ts:132-145,205-225,347-370,373-392`, `components/run-diagnosis.tsx:27` | route facts, `ScopedDispatchDisabled` | D | ~90 |
| `cypress/e2e/celln-own-key.cy.ts` 476 | tests at 84/99/107 delete; route flows rewrite; line 31 guard moot | D | ~250 net |
| `cypress/e2e/run-diagnosis.cy.ts:178-203,223` | route-mismatch / ScopedDispatchDisabled cases | D | ~60 |
| `package.json` `test:stubbed` | drop `celln-fleet-setup` | D | 1 |

## 9. Docs

| Page | Action |
|---|---|
| `docs/guides/celln-mediated-model-access.md` 621 | replace |
| `docs/guides/celln-fleet-installation.md` 978 | rewrite; drop 43–97, 231–462, 495–545 (~500) |
| `docs/concepts/celln-backend.md` 205 | rewrite 62–89, 163–186 |
| `docs/guides/celln-model-gateway-component-installation.md` 81 | delete (gateway default-on) |
| `docs/reference/cli.md` (16 flag rows), `docs/guides/model-connections.md` (5), `docs/concepts/skills-tools-and-execution.md` (3), `README.md` (2), `docs/getting-started.md` (2), `docs/architecture.md` (1), `internal/modelgateway/README.md` | edit |
| `docs/design/celln-namespace-authorisation.md`, `celln-policy-resolution.md`, `celln-scoped-artifacts.md` | "superseded by" note |
| `mkdocs.yml:148-153` | nav |
| `docs/evidence/celln-fleet-kind-2026-09-14.md` 476 | keep, mark superseded |
| **A**: `docs/guides/celln-external-router-migration.md` 87, `docs/guides/celln-native-installation.md` 317 | delete |

## 10. Integration scripts

`test/integration/test-celln-mediated-fleet.sh` 622 and `test-celln-fleet.sh`
642 → one two-team journey; `test-celln-oneliner.sh` 143 → rewrite;
`celln-installed-qualification/LOCAL-RUNBOOK.md` AUTH_ROUTE_MISMATCH note. None
are in CI workflows.

## Already dead or vestigial today (independent of the design)

| # | Item | Evidence | LOC |
|---|---|---|---|
| A1 | One-shot router / single dispatcher plane: `templates/celln.yaml` 680 (node-held provider keys 184–191 from `celln.*ApiKey`), values `celln.installer/dispatcher/router.external/bootstrap/routerUrl/tokenSecret/*ApiKey`, `main.go:1404-1480` `--celln-host-installer`/`--celln-backend`, `install_celln_test.go` 121, `charts/celln_single_plane_test.go` 99, `celln_dispatcher_test.go` 76, `controller/celln_external_router_test.go` 31, `config/host/*` systemd units (477) | `celln-fleet.yaml:3-4` refuses to coexist; fleet sets `celln.dispatcher.enabled=false`. Keep `images/celln-installer` (also the node-configure image). | ~1,500 |
| A2 | `--celln-native` single managed plane: `main.go:1337-1356,1411-1420`, `celln_native_install_cmd.go` 35, `cellninstall/install.go` 263 + `plane.go` 121 + tests 246, `templates/celln-native-parent.yaml` 182, `charts/celln_native_parent_test.go` 250, values `celln.nativeParent` (515–542), `docs/guides/celln-native-installation.md` 317 | referenced only by `docs/reference/cli.md:71`; `Partitioned` has 0 callers | ~1,450 |
| A3 | Legacy namespaced issuer/review stack: `internal/cellnreview` 6,284, `cellnauthority/{model,loader,plan,frozen,tools,execution,preview}.go` ~930, `cmd/sympozium/celln_{issuer,withdraw,tool,selection}_cmd.go` 470 (+tests ~240), `cmd/celln-review-ui` 414, `internal/cellnevidence` 245, `controller/celln_catalogue.go` 243 (+363 test), `apiserver/celln_preview.go`, `server.go:499`, web `celln-review` page, `celln-selection.cy.ts` 209, `celln-review-live.cy.ts`, `celln-scoped-review.cy.ts`, `charts/celln_tool_ui_test.go` 92, `celln.harnessEnabled` | `cmd/controller/main.go:238` wires it only for "execution recovery; new submission requires CELLN_HARNESS_ENABLED=true" | ~10,000 (separate decision) |
| A4 | Dead functions in §2/§3 | 0 non-test callers | ~100 |
| A5 | `celln.openaiBaseUrl` | no template read | 2 |
| — | v1alpha1 harness adapter contract | **not dead**: still the `job`-backend harness mode | |

## Totals

- Design-removed: ≈ **14,000 LOC** (Go ~8,000 incl. tests; chart + scripts
  ~2,100; web ~1,000; docs ~1,300; integration ~1,400 rewritten).
- Already dead: ≈ **3,000 LOC** (A1 + A2 + A4 + A5), plus **~10,000** for the
  legacy issuer stack (A3) if that decision is taken.

Ten largest: the two fleet journeys (1,264, rewrite as one); `fleet.go` + test
(~1,000); `starter_agent` + `mediated_backends` + `mediation_record` +
`mediation_default_test` (~1,140); the two guides (~1,100); `platform.go` +
tests (~900); `celln_backends*.go` (898); `cellnparent/platform.go` + tests
(~930, pending decision); `charts/celln_mediation_test.go` (763);
`celln_fleet_install.go` + tests (~1,000); `model_parameters` +
`model_output_tokens` + tests (~950).

Ordering: trust hook Job before `celln-mediation bootstrap`/`planMediation`
go; single-profile `cellnplatform` before `celln_backends.go`/`TenantWrappers`
removal; CRD `Routes` → origin allow/deny before the resolver and
`_helpers.tpl` route validation are deleted; decide `cellnparent` (§5) before
touching `celln_scoped.go` route classification.
