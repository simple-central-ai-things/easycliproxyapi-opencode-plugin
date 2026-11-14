# AGENTS.md — easycliproxyapi-opencode-plugin

Native Go plugin for CLIProxyAPI/EasyCLI that exposes **OpenCode Go** as a
single provider (`opencode-go`) inside the org's EasyCLI stack
(simple-central-proxy-manager `easycli/`, whose CLIProxyAPI process runs in
the single SCPM `gateway` container — no standalone EasyCLI container and no
EasyCLI host port): one model
catalog, one key pool, protocol translation across OpenAI Chat Completions,
Anthropic Messages and OpenAI Responses, config-authoritative credential
reconciliation, and host-native quota cards rendered on the EasyCLI credential
cards (no plugin-owned management page).

This repo keeps its functional `easycliproxyapi-<provider>-plugin` name — no SC
short name — because the plugin id and the plugin-store-polled repo URL are
bound to the binary identity (org brand-rule exception, see the folder-root
`AGENTS.md`).

## Provenance: re-derived v2.3.0 source

This repo holds the **re-derived v2.3.0 source** of the deployed plugin. The
original v2.3.0 tree was never pushed anywhere; this tree reconstructs the
deployed surface from the public config contract, the CLIProxyAPI v8 SDK and
the sibling provider plugin. Evidence chain: `PROVENANCE.md`.

- plugin id `easycliproxyapi-opencode-provider` (`internal/plugin/plugin.go`, `registry.json`,
  CI `PLUGIN_ID`) — the EasyCLI `plugins.configs` contract key
- library filename `easycliproxyapi-opencode-provider.so` / `.dll` / `.dylib`
  (CI `LIB_BASE`), matching the deployed artifact
- config block `plugins.configs.easycliproxyapi-opencode-provider` with `api-keys` + a
  `plans:` list (per-plan base-url / model prefix, e.g. `go` → `opencode-go`,
  `zen` → `opencode-zen`)
- CLIProxyAPI **v8** SDK (`github.com/router-for-me/CLIProxyAPI/v8 v8.0.0`),
  RPC schema 6, native C ABI version 1
- quota renders through the v8 `QuotaProvider` native cards; the plugin ships
  no management page or management route
- Go module `opencode-go-cliproxyapi`; plugin version `2.3.0`

Verification is **offline only** (`internal/plugin/abi_test.go`, the config,
catalog and quota suites, and the `internal/loadsmoke` dlopen dry-run of the
built artifact). Binary equality with the running `.so` is not attested; deployment stays an operator action and must compare this build's
registration surface with the live plugin's registration log first.

## Layout

```text
main.go                    CGO glue: cliproxy_plugin_init / cliproxyPluginCall ABI exports; every RPC forwarded to internal/plugin.HandleCall
registry.json              plugin-store identity: id easycliproxyapi-opencode-provider, version 2.3.0, this repository
PROVENANCE.md              evidence chain for the re-derived v2.3.0 surface
CHANGELOG.md               running release history (RELEASE_NOTES.md is the current release body)
internal/plugin/           dispatcher, registration metadata, executor, credential reconciliation, v8 quota provider
internal/adapter/          protocol translation: chatcompletions/, messages/, responses/, shared/
internal/catalog/          per-plan model catalog discovery + normalization
internal/config/           plugins.configs.<id> schema: api-keys, plans (name/base-url/catalog-url/model-prefix/enabled), catalog, protocols, route-overrides, request-timeout, max-response-bytes, allow-http
internal/errclass/         error classification + explainable upstream error reporting
internal/thinking/         numeric Anthropic budget -> effort-level conversion (explicit effort values never touched)
internal/loadsmoke/        dlopen() load smoke: the built .so through the real C ABI against a fake host (offline; skips when the artifact is absent)
tests/live_*_test.go       live-environment regression tests behind the `live` build tag (CI runs offline)
.github/workflows/         tests.yml (glibc c-shared build, then vet + test) and build.yml (7-target release matrix + tag releases)
.github/scripts/           package-release.go (release zip + sha256 packaging)
```

## Contracts

- **Plugin id: `easycliproxyapi-opencode-provider`** (registration metadata, plugin-store id
  and the EasyCLI config key). Provider id: `opencode-go`; each enabled plan
  publishes its own model prefix (`opencode-go/<model>`, `opencode-zen/<model>`).
- **Library filename:** `easycliproxyapi-opencode-provider` (CI `LIB_BASE`) —
  the deployed artifact name. The filename is not part of the runtime identity.
- **Version:** `2.3.0`, in lockstep across `plugin.go`, `registry.json` and
  `RELEASE_NOTES.md`.
- **Config**: `plugins.configs.easycliproxyapi-opencode-provider` in the EasyCLI
  `config.yaml`. `api-keys` is required, shared by every plan and supports
  `${ENV_VAR}` expansion (`os.ExpandEnv` at config load). Plans are declared
  under `plans:`; a config without `plans:` loads as one implicit `go` plan.
  Full option table: README → Configuration.
- **Env keys (names only)**: the live deployment feeds
  `OPENCODE_GO_API_KEY_1/2` from the SCSM `tag:easycli` export rendered by
  SCPM `easycli/deploy.sh` into the container `.env`. No host-specific
  values in this repo.
- **Auth records**: each configured key is materialized as
  `opencode-go-key-<sha256(api_key)>.json` in the CPA auth directory; every
  register/reconfigure reconciles records against the config (create/enable,
  staged prove-then-delete for removals). OAuth Console login (device code
  flow) adds a second plugin-owned credential type —
  `opencode-oauth-<sha256>.json` with `access_token`/`refresh_token`/
  `org_id`, refreshed via `grant_type=refresh_token` — that reconciliation
  never touches (legacy pre-v2.5.2 `opencode-go-oauth-<sha256>.json`
  records stay detectable until migrated); a credential is either an API key or an OAuth token (routed
  on the `credential_type` attribute), and the executor sends whichever as
  the upstream bearer, adding `x-opencode-org-id` + `User-Agent` for OAuth.
  `executor.count_tokens` and `executor.http_request` return `unsupported`.
- **Quota**: v8 `QuotaProvider` (`quota.identifier` / `describe` / `fetch` /
  `reset`) rendered on the host's native credential cards; `quota.reset`
  reports `unsupported`. No plugin-owned management page or route.
- **SDK/ABI**: `github.com/router-for-me/CLIProxyAPI/v8 v8.0.0`; C ABI version 1,
  RPC schema 6; `cliproxy_plugin_init` rejects a host ABI version mismatch.
  Registration metadata must keep `GitHubRepository` set (host `validPlugin`
  gate).
- **Cross-repo**: the EasyCLI stack (config template, deploy, SCSM `tag:easycli`
  rendering) is owned by `simple-central-proxy-manager/easycli/`; its
  CLIProxyAPI process runs inside that repo's single `gateway` container (no
  standalone EasyCLI container, no EasyCLI host port). This repo only ships
  the plugin source.

## Build / test / run

```sh
go vet ./...
# Build first: internal/loadsmoke dlopen()s the artifact during go test and
# skips, with a printed reason, when it is absent.
TMPDIR=$HOME/.cache/opencode/tmp CGO_ENABLED=1 \
  go build -buildmode=c-shared -o plugins/linux/amd64/easycliproxyapi-opencode-provider.so .
go test ./...                       # offline; tests/live_*_test.go are behind the `live` build tag
gofmt -l .

# local development build (debug tag per CONTRIBUTING.md)
TMPDIR=$HOME/.cache/opencode/tmp CGO_ENABLED=1 \
  go build -tags debug -buildmode=c-shared -o plugins/linux/amd64/easycliproxyapi-opencode-provider.so .
```

- Machine rules: no `/tmp` (RAM tmpfs) — use `TMPDIR=$HOME/.cache/opencode/tmp`
  and `GOCACHE` under it for Go builds; no-rm (trash-move, never hard-delete).
- CI: `.github/workflows/tests.yml` builds the glibc linux/amd64 c-shared
  artifact FIRST, then runs vet + tests on every push/PR, so
  `internal/loadsmoke` dlopen()s the real `.so` (the SCPM gateway container is
  Debian-based, so musl builds will not `dlopen`). `.github/workflows/build.yml`
  runs the 7-target matrix (linux amd64/arm64, darwin amd64/arm64, windows
  amd64, windows arm64, freebsd amd64) and publishes a GitHub release from
  `RELEASE_NOTES.md` on `v*` tag push.
- Release checklist (CONTRIBUTING.md): bump `pluginVersion` in
  `internal/plugin/plugin.go` **and** `version` in `registry.json` (the store
  falls back to it), add the `CHANGELOG.md` entry, rewrite `RELEASE_NOTES.md`
  for the new version, commit `chore(release): vX.Y.Z`, push main, then push
  the annotated tag. Do not rename the CI `.so` artifact
  (`easycliproxyapi-opencode-provider`): the store-polled repo URL and the
  plugin id are bound to it.

## Deploy

Hand install — the live deployment is not installed through the plugin store:

1. Build the glibc library: `CGO_ENABLED=1 go build -buildmode=c-shared -o
   plugins/linux/amd64/easycliproxyapi-opencode-provider.so .` (TMPDIR per
   machine rules).
2. Copy it into the EasyCLI bind-mounted plugins directory
   (`plugins/linux/amd64/` next to the EasyCLI `config.yaml` on `<deploy-host>`).
3. Ensure a `plugins.configs.easycliproxyapi-opencode-provider` block with env-expanded
   `api-keys` and the intended `plans:` exists in the EasyCLI `config.yaml`
   (panel-managed; see SCPM `easycli/` for template + deploy scripts).
4. Restart the SCPM `gateway` container (`easycli/deploy.sh`); verify the
   plugin registers (catalog refresh log line) before relying on a plan's
   `<prefix>/<model>`.

**Verification before replacement:** this source was verified offline only.
Compare the build's registration surface (plugin id, version, capabilities)
with the live plugin's registration log before overwriting a running artifact.
Real host values live only in the private ops doc (`.opencode/ops/`, folder
level).

## Conventions

- English only; no Cyrillic (scan: `grep -rnP '[\x{0400}-\x{04FF}]'`).
- No host-specific details in this public repo: no LAN IPs, ssh targets, host
  nicknames or deploy-host directory layout — placeholders only.
- Secrets never in commits; `api-keys` in examples must be placeholders.
- Commit identity `hello@babico.name.tr`; commit + push after every meaningful
  change; stage only explicit paths (no `git add -A`).
- Docs: `README.md` is the product doc; `RELEASE_NOTES.md` is the release body
  (published verbatim); keep `registry.json` version in lockstep with the tag.

## Cross-repo links

- `simple-central-proxy-manager` (`easycli/`) — EasyCLI config template +
  deploy, SCSM `tag:easycli` rendering; its CLIProxyAPI process runs inside that
  repo's single `gateway` container.
- `simple-central-agent-config` (SCACFG) — consumer harness configs that route
  through the gateway.
- `simple-central-ai-things/docs` — public architecture hub; per-repo page
  `repos/easycliproxyapi-opencode-plugin.md` (live v2.3.0 identity; update it
  when the cross-repo contract changes — see `PROVENANCE.md`).
- Org brand rules + repo graph: the folder-root `AGENTS.md`.
