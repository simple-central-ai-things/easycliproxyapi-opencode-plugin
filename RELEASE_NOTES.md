## What's Changed

### Re-derived v2.3.0 on the CLIProxyAPI v8 plugin SDK

This release replaces the stale v1.0.3 tree with a re-derivation of the deployed
v2.3.0 plugin surface. See `PROVENANCE.md` for the full evidence chain.

- **SDK generation:** `github.com/router-for-me/CLIProxyAPI/v8 v8.0.0` (RPC
  schema 6). The native C ABI shape is unchanged (`ABIVersion == 1`), so the
  CGO glue keeps exporting `cliproxy_plugin_init`, `cliproxyPluginCall`,
  `cliproxyPluginFree` and `cliproxyPluginShutdown`.
- **Config schema:** new `plans:` list. Each enabled plan owns a base URL, a
  catalog URL (`{base-url}/models` by default) and its own model prefix
  (`go` → `opencode-go`, `zen` → `opencode-zen`). One `api-keys` pool serves
  every plan. Legacy single-plan configs (`base-url` + `model-prefix`) still
  load as one implicit plan named `go`.
- **Per-plan catalog and routing:** every catalog refresh fetches each enabled
  plan's catalog; a disabled plan is neither fetched nor routable. Each model
  record carries its plan name and base URL, so execution joins the endpoint
  onto the plan's own upstream.
- **Quota surface:** the plugin-owned management page and `quota-usage` route
  are gone. Quota now renders on the host's native credential cards through the
  v8 `QuotaProvider` interface (`quota.identifier` / `describe` / `fetch` /
  `reset`), with the rolling, weekly and monthly windows normalized into
  buckets. `quota.reset` reports `unsupported`.
- **Quota card parity:** the card shape matches the sibling
  `easycliproxyapi-commandcode-provider` plugin — subscription plan label, a
  plan summary metric and one bucket per reported window. The credential key
  resolves from the parsed `api_key` attribute and falls back to host-managed
  `metadata`; remaining fractions are clamped to `0..1`; a window the upstream
  never described is omitted instead of being drawn as a full bar.
- **Identity:** runtime plugin id stays `opencode-go-provider` (the EasyCLI
  config-contract key); the shared library is published as
  `easycliproxyapi-opencode-provider` to match the deployed artifact. Version
  is `2.3.0`; `registry.json` and the repository URL now point at this
  repository instead of the upstream fork.
- **Release prep:** `pluginVersion`, `registry.json` and the release docs are
  in lockstep at `2.3.0`, and an offline test enforces that lockstep so a
  version bump cannot drift. The annotated `v2.3.0` tag and the GitHub release
  are a separate operator step; CI builds the release matrix and publishes this
  file as the release body on a `v*` tag push.

### Tests (offline, no network, no secrets)

- ABI contract pins: ABI version, RPC schema version, registration identity,
  version, repository and capability set (including `quota_provider`, excluding
  `management_api`).
- Plan schema: template-shaped YAML, per-plan prefixes, disabled plans,
  duplicate/ambiguous plan rejection and legacy single-plan compatibility.
- Plan-aware catalog: per-plan refresh, stale retention for one failing plan,
  fail-closed for an all-plan outage, and per-record upstream base URLs.
- Quota card surface: identifier/describe/fetch/reset envelopes, normalized
  response shape, plan selection by credential attribute, credential
  redaction and upstream-failure classification; parity with the sibling
  plugin on the metadata key fallback, remaining-fraction clamping and
  omission of unreported windows.
- Release identity: `registry.json` (plugin id, version, repository) and the
  release docs are pinned to the `plugin.go` constants, so a version bump
  cannot drift from the docs.
- Auth records: the canonical record written by reconciliation parses back
  through `auth.parse`.

### Upgrade Notes

- Config migration: move `base-url`/`model-prefix` under a `plans:` entry (or
  keep them as the legacy single-plan shorthand). `api-keys` is unchanged.
- The model names are unchanged for the `go` plan; a second enabled plan adds
  its own prefix namespace.
- Credential records (`opencode-go-key-<sha256(api_key)>.json`) are unchanged;
  reconciliation still creates, re-enables and removes them from `api-keys`.
- This repository is source-only for this release. Do not overwrite a running
  deployment without comparing this surface against the live plugin's
  registration log first.

**Full Changelog**: https://github.com/simple-central-ai-things/easycliproxyapi-opencode-plugin/compare/v1.0.3...v2.3.0
