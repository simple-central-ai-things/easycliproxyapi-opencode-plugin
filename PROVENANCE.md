# Provenance — re-derived v2.3.0 (CLIProxyAPI v8, `plans:` schema)

This repository ships the **re-derived** source of the EasyCLI OpenCode Go
provider plugin whose deployed binary reports **v2.3.0**. It is not a recovery
of the original commit history (that source was never pushed anywhere); it is a
reconstruction that matches the deployed surface, with the evidence for every
decision recorded here.

Nothing in this file is host-specific: no deployment host, container paths,
keys, or LAN addresses. Every value below is a public contract or a synthetic
example.

## v2.4.0 builds on this surface

The re-derived v2.3.0 surface above remains the ABI and config baseline. The
v2.4.0 release adds free-tier session fingerprinting (genuine-OpenCode-shaped
bodies, client-identity headers, exact-format session ids), OAuth device-code
console login as a second credential type, and native-envelope catalog limits.
v2.5.0 makes `api-keys` optional so OAuth console login can be the sole
credential source, with the catalog fetch falling back to an OAuth access
token when no API key is configured. v2.5.1 names quota-card buckets after
the window they meter (`rolling`, `weekly`, `monthly`) instead of the
window's current status token, so consumers can tell the limits apart.
None of these change the wire ABI, the registration surface, or the artifact
filename; the `plans:`/`api-keys` config contract is unchanged except that
`api-keys` is now optional. The reported version moves
2.3.0 -> 2.4.0 -> 2.5.0 -> 2.5.1.

## What "matches the deployed v2.3.0" means here

| Surface | Deployed (canonical) | This source |
| --- | --- | --- |
| Runtime plugin id | `easycliproxyapi-opencode-provider` (EasyCLI `plugins.configs` key) | `pluginName` in `internal/plugin/plugin.go`, `registry.json` |
| Shared-library filename | `easycliproxyapi-opencode-provider.so` | CI `LIB_BASE` (build output name) |
| Version | `2.3.0` | `pluginVersion`, `registry.json`, `RELEASE_NOTES.md` |
| Host SDK | CLIProxyAPI **v8** native plugin ABI | `go.mod` → `github.com/router-for-me/CLIProxyAPI/v8 v8.0.0` |
| RPC schema | v8 schema 6 | `pluginabi.SchemaVersion` (asserted in `internal/plugin/abi_test.go`) |
| Config schema | `api-keys` (one pool) + `plans:` (per-plan base-url / model prefix, e.g. `go` → `opencode-go`, `zen` → `opencode-zen`) | `internal/config` plan schema + tests |
| Quota surface | plugin-owned v8 `QuotaProvider` (native credential cards), **no** plugin management page | `internal/plugin/quota.go`, capability `quota_provider` |
| Auth records | `opencode-go-key-<sha256(api_key)>.json`, reconciled via host auth list/save | `internal/plugin/auth_reconcile.go` (unchanged behaviour) |

## Evidence chain

1. **Config contract (identity + plan schema).**
   `simple-central-proxy-manager/easycli/config.yaml.template` writes the block
   under `plugins.configs.easycliproxyapi-opencode-provider` with a `plans:` list
   (`name`, `base-url`, `model-prefix`, `enabled`, operator `note`) and a single
   `api-keys` pool, and the proxy's `easycli/tests/test_config_contract.py`
   pins that id (and rejects the older `easycli-proxyapi-opencode-provider`
   spellings). That is why the runtime id stayed `easycliproxyapi-opencode-provider` while
   the deployed file is `easycliproxyapi-opencode-provider.so`.
2. **SDK generation (v8).** The deployed plugin runs under a CLIProxyAPI v8
   host: v8 is the first SDK generation with the native `QuotaProvider`
   interface, `plugin.quiesce`, schema version 6, and the
   `quota.identifier/describe/fetch/reset` RPCs. A v7 build cannot expose the
   v2.3.0 quota card surface, so the source was moved to `v8.0.0`. The C ABI
   struct itself is unchanged (`pluginabi.ABIVersion == 1`), which is why the
   existing `main.go` CGO glue only needed the import bump.
3. **Plan schema shape.** Derived from the template above (two plans, one
   disabled pay-as-you-go plan) plus the deployed model namespaces
   (`opencode-go/<model>`, `opencode-zen/<model>`). Plan semantics implemented
   here:
   - one key pool serves every plan (`api-keys` is global);
   - each enabled plan owns a base URL, a catalog URL (`{base-url}/models`
     unless overridden) and a model prefix;
   - a plan with `enabled: false` is neither fetched nor routable;
   - public IDs are `prefix/model` per plan; bare upstream IDs still resolve
     for panel-chosen requests;
   - legacy single-plan configs (`base-url` + `model-prefix`, no `plans:`)
     load as one implicit plan named `go`, so v1.0.3-era configs do not break.
4. **Sibling pattern.** `easycliproxyapi-commandcode-plugin` is the sibling
   provider already on the v8 SDK. It establishes the v8 conventions this
   source follows: `quota_provider` capability, `quota.*` method dispatch,
   `quotaMaxTimeout`, host-rendered quota cards instead of a plugin-owned HTML
   page, `model.for_auth` handled separately from `model.static`, and a
   `GitHubRepository` that points at the org repository the store polls.
   Its source comment records that the OpenCode plugin also renders quota on
   the host's native cards, corroborating point 2.
5. **Upstream lineage.** The protocol adapters, catalog normalisation, credential
   reconciliation and error classification are the v1.0.3 sources
   (`github.com/xspeed1989/opencode-go-cliproxyapi`, MIT, itself descended from
   `github.com/massiveits/opencode-go-cliproxyapi`). They were kept as-is except
   for the SDK import bump, the plan-aware catalog/executor seam, and the quota
   surface swap.

## What is verified offline (and what is not)

Verified by `go test ./...` in CI-equivalent conditions (glibc linux/amd64,
`CGO_ENABLED=1 go build -buildmode=c-shared`), with no network and no secrets:

- ABI contract: `ABIVersion == 1`, `SchemaVersion == 6`, registration identity,
  version, repository URL, capability set (`model_provider`, `auth_provider`,
  `executor`, `quota_provider`; no `management_api`).
- Plan schema: template-shaped YAML loads; per-plan prefixes publish distinct
  model IDs; disabled plans are not fetched; duplicate names/prefixes and
  all-disabled plan lists are rejected; legacy single-plan configs still load.
- Routing seam: each model record carries its plan name and base URL, and the
  executor joins the endpoint onto that plan's upstream.
- Quota card surface: `quota.identifier` / `quota.describe` / `quota.fetch` /
  `quota.reset` envelopes, normalized `QuotaFetchResponse` shape, plan
  selection by credential attribute, and redaction of the credential.
- Auth records: the canonical record written by reconciliation parses back
  through `auth.parse` with the same id and without leaking the key.

**Not attested:** binary-level equality with the deployed `.so` (no build of the
original exists to compare), and live host behaviour. This repository never
builds or deploys over the running artifact — deployment is an operator action
on the deploy host, and only after comparing this source's surface with the
live plugin's registration log.

## Reproducing the build

```sh
export TMPDIR=$HOME/.cache/opencode/tmp GOCACHE=$HOME/.cache/opencode/tmp/gocache
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -o plugins/linux/amd64/easycliproxyapi-opencode-provider.so .
```

The resulting shared object exports `cliproxy_plugin_init`,
`cliproxyPluginCall`, `cliproxyPluginFree` and `cliproxyPluginShutdown` — the
same four symbols the v8 host loader requires.
