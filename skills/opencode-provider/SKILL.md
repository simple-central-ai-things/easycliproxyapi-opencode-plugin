---
name: opencode-provider
description: Use when building, debugging, testing, or deploying the OpenCode Go EasyCLI provider plugin (easycliproxyapi-opencode-plugin), or when touching the opencode-go provider inside the EasyCLI/CLIProxyAPI bridge (SCPM easycli-proxy, port 8792). Trigger words: opencode plugin, opencode-go-provider, easycliproxyapi-opencode-plugin, OpenCode Go provider, opencode-go model prefix, OpenCode Go quota page, CPA provider plugin .so.
---

# OpenCode provider plugin

Native Go CLIProxyAPI/EasyCLI plugin that exposes OpenCode Go as a single
provider (`opencode-go`) inside the EasyCLI bridge: one model catalog, one key
pool, multi-protocol translation (Chat Completions / Anthropic Messages /
Responses), config-authoritative credential reconciliation, and an OpenCode Go
quota page in the management panel.

## Key paths

- `easycliproxyapi-opencode-plugin/` — repo checkout
  (github.com/simple-central-ai-things/easycliproxyapi-opencode-plugin)
- `main.go` — CGO glue: `cliproxy_plugin_init` / `cliproxyPluginCall` ABI exports
- `internal/plugin/plugin.go` — dispatcher + identity constants
  (`ProviderID`, `pluginName`, `pluginVersion`); release checklist bumps this
- `internal/plugin/auth_reconcile.go` — credential reconciliation
- `internal/adapter/{chatcompletions,messages,responses,shared}/` — protocol translation
- `internal/catalog/` — model catalog discovery; `internal/config/` — config schema
- `registry.json` — plugin-store identity (this repository, version 2.3.0)
- `PROVENANCE.md` — evidence chain for the re-derived v2.3.0 surface
- `tests/live_*_test.go` — behind the `live` build tag; CI runs offline
- `simple-central-proxy-manager/easycli-proxy/` — the EasyCLI stack this plugin runs in
  (config template, deploy.sh, `:8792`, SCSM `tag:easycli` rendering)

## Contracts / env

- Plugin id: `opencode-go-provider` (registration metadata, store id and the
  EasyCLI config key). Library filename: `easycliproxyapi-opencode-provider`
  (CI `LIB_BASE`) — the deployed artifact name, not the runtime identity.
- Provider id: `opencode-go`; each enabled plan publishes its own model prefix
  (`opencode-go/<model>`, `opencode-zen/<model>`). Bare upstream IDs also resolve.
- Config: `plugins.configs.opencode-go-provider` with a shared `api-keys` pool
  and a `plans:` list (`name`, `base-url`, `catalog-url`, `model-prefix`,
  `enabled`, `note`). A config without `plans:` loads as one implicit `go` plan
  from `base-url`/`model-prefix`. Shared: `catalog`, `protocols`,
  `route-overrides`, `request-timeout`, `max-response-bytes`, `allow-http`.
- `api-keys` values support `${ENV_VAR}` expansion (`os.ExpandEnv`). Live keys
  are `OPENCODE_GO_API_KEY_1/2`, rendered from the SCSM `tag:easycli` export by
  SCPM `easycli-proxy/deploy.sh` (names only — values never in this repo).
- Auth records: `opencode-go-key-<sha256(api_key)>.json` in the CPA auth dir;
  reconciled on every register/reconfigure; removals use staged prove-then-delete.
- Quota: v8 `QuotaProvider` native cards (`quota.identifier` / `describe` /
  `fetch` / `reset`); no plugin-owned management page. `quota.fetch` uses the
  credential attribute `plan`, else the first enabled plan; `quota.reset` is
  `unsupported`. API-key only: auth login, `executor.count_tokens` and
  `executor.http_request` return `unsupported`.
- SDK/ABI: `github.com/router-for-me/CLIProxyAPI/v8 v8.0.0`, C ABI version 1,
  RPC schema 6; `cliproxy_plugin_init` rejects an ABI version mismatch;
  registration metadata must keep `GitHubRepository` set (host `validPlugin` gate).

## Common operations

```sh
cd easycliproxyapi-opencode-plugin
go vet ./... && go test ./...   # offline; live_*_test.go need the `live` tag
TMPDIR=$HOME/.cache/opencode/tmp CGO_ENABLED=1 \
  go build -tags debug -buildmode=c-shared -o plugins/linux/amd64/easycliproxyapi-opencode-provider.so .
```

- Always `-tags debug` for local builds (CONTRIBUTING.md); release packaging is
  CI's job. Machine rule: never `/tmp` (RAM tmpfs) — `TMPDIR=` / `GOCACHE=`
  under `~/.cache/opencode/tmp`.
- CI: `.github/workflows/tests.yml` (vet + test + glibc linux/amd64 c-shared
  build on push/PR); `.github/workflows/build.yml` (7-target matrix + tag
  release from `RELEASE_NOTES.md`). Release = bump `pluginVersion` +
  `registry.json` version, rewrite `RELEASE_NOTES.md`, tag `vX.Y.Z`.
- Deploy is a hand install (not store-managed): glibc `.so` into the EasyCLI
  bind-mounted `plugins/linux/amd64/`, `plugins.configs.<id>` block in the
  EasyCLI config (panel-managed), recreate the `easycli-api` container.
  Real host values: folder-level `.opencode/ops/local-infrastructure.md` (private).

## Gotchas

- **Re-derived, offline-verified only.** This repo reconstructs the deployed
  v2.3.0 surface (v8 SDK, `plans:` schema, native quota cards); the original
  tree was never pushed. `PROVENANCE.md` records the evidence chain. Do not
  assume binary equality with the running `.so`: compare a build's registration
  surface with the live registration log before replacing an artifact.
- The plugin-store identity is this repo's: `registry.json` `repository` points
  at `simple-central-ai-things/easycliproxyapi-opencode-plugin`.
- Credential reconciliation is best-effort on purpose: housekeeping failures
  are `warn`s; a failed registration would drop the whole provider.
- The EasyCLI config is panel-managed; SCPM's template/tests pin
  `opencode-go-provider` and the `plans:` shape — template edits prove nothing
  about the running host.
- Registration metadata with an empty `GitHubRepository` is rejected by the
  host `validPlugin` gate.
