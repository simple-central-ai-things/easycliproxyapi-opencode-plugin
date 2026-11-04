# Changelog

User-facing release summaries for the OpenCode Go EasyCLI provider plugin.
`RELEASE_NOTES.md` carries the body of the current release; this file keeps
the running history. Releases before v2.3.0 are reachable through the git tags
(`v0.1.x` upstream lineage, `v1.0.0`–`v1.0.3`).

## v2.3.0 — 2026-11-04 (re-derived deployed surface, tag-ready)

Re-derivation of the v2.3.0 plugin surface running in the EasyCLI
deployment. The original v2.3.0 tree was never pushed; this release
reconstructs it from the public config contract, the CLIProxyAPI v8 SDK and
the sibling provider plugin. See `PROVENANCE.md` for the evidence chain.

- **SDK generation:** `github.com/router-for-me/CLIProxyAPI/v8 v8.0.0` (RPC
  schema 6). The native C ABI shape is unchanged (`ABIVersion == 1`), so the
  CGO glue keeps exporting `cliproxy_plugin_init`, `cliproxyPluginCall`,
  `cliproxyPluginFree` and `cliproxyPluginShutdown`.
- **Config schema:** new `plans:` list. Each enabled plan owns a base URL, a
  catalog URL (`{base-url}/models` by default) and its own model prefix
  (`go` → `opencode-go`, `zen` → `opencode-zen`). One `api-keys` pool serves
  every plan. A config without `plans:` still loads as one implicit `go`
  plan, so v1.0.3-era configs keep working.
- **Per-plan catalog and routing:** every refresh fetches each enabled plan's
  catalog; a disabled plan is neither fetched nor routable. Model records
  carry their plan name and base URL, so execution joins the endpoint onto the
  plan's own upstream.
- **Quota cards:** the plugin-owned management page and `quota-usage` route
  are gone. Quota renders on the host's native credential cards through the
  v8 `QuotaProvider` interface (`quota.identifier` / `describe` / `fetch` /
  `reset`), with the rolling, weekly and monthly windows normalized into
  buckets. `quota.reset` reports `unsupported`.
- **Quota card parity with the sibling plugin:** the card carries the same
  normalized shape as `easycliproxyapi-commandcode-provider` — a subscription
  plan label, a plan summary metric and one bucket per reported window. The
  credential key resolves from the parsed `api_key` attribute and falls back
  to host-managed `metadata` (the window between `host.auth.save` and the
  auth-directory re-parse), remaining fractions are clamped to `0..1`, and a
  window the upstream never described is omitted instead of being drawn as a
  full bar. No real API calls are made by the offline tests.
- **Identity:** runtime plugin id stays `opencode-go-provider` (the EasyCLI
  config-contract key pinned by `simple-central-proxy-manager/easycli/tests`);
  the shared library is published as `easycliproxyapi-opencode-provider` to
  match the deployed artifact. Version is `2.3.0`; `registry.json` and the
  repository URL point at this repository.
- **Release prep:** `pluginVersion`, `registry.json` and the release docs are
  in lockstep at `2.3.0` and an offline test enforces that lockstep. The
  annotated `v2.3.0` tag is not pushed by this change; CI publishes the
  release from `RELEASE_NOTES.md` on a `v*` tag push.

### Upgrade notes

- Config migration: move `base-url`/`model-prefix` under a `plans:` entry (or
  keep them as the legacy single-plan shorthand). `api-keys` is unchanged.
- Model names are unchanged for the `go` plan; a second enabled plan adds its
  own prefix namespace.
- Credential records (`opencode-go-key-<sha256(api_key)>.json`) are
  unchanged; reconciliation still creates, re-enables and removes them from
  `api-keys`.
- This release is source-only: compare a new build's registration surface
  against the live plugin's registration log before replacing a running
  artifact.
