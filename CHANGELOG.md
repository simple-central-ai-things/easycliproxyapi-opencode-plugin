# Changelog

User-facing release summaries for the OpenCode Go EasyCLI provider plugin.
`RELEASE_NOTES.md` carries the body of the current release; this file keeps
the running history. Releases before v2.3.0 are reachable through the git tags
(`v0.1.x` upstream lineage, `v1.0.0`–`v1.0.3`).

## v2.5.3 — 2026-11-14 (duration-style window labels)

Quota buckets now use the shared duration convention: the rolling window
is `5h`, the weekly window `7d` and the monthly window `30d`, matching
the native Codex card labels. Values, resets and the wire ABI are
unchanged.

## v2.5.2 — 2026-11-14 (OAuth credential naming: opencode-oauth-)

Plugin-owned OAuth console credentials are now written as
`opencode-oauth-<sha256>.json` instead of `opencode-go-oauth-<sha256>.json`
(operator naming preference — the provider is opencode-go, the credential
is an OpenCode OAuth login, not a "go" variant). Existing legacy-prefix
records keep working: detection accepts both prefixes, and a re-login for
the same account writes the new name. No wire ABI, registration-surface
or config change.

## v2.5.1 — 2026-11-14 (quota buckets named after their window)

Quota-card buckets carried the window's current STATUS ("ok"/"exceeded")
in the `window` field, so every bucket read "ok" and consumers could not
tell the rolling, weekly and monthly limits apart. Buckets are now named
after the window they meter (`rolling`, `weekly`, `monthly`); the state
rides `remaining_fraction` as before. No wire ABI, registration-surface
or config change.

## v2.5.0 — 2026-11-10 (OAuth can be the sole credential source)

Makes `api-keys` optional so an opencode-go provider configured with only an
OAuth console login (no API keys) loads and serves models.

- **Optional `api-keys`:** the config no longer rejects an empty `api-keys`
  list. An OAuth-only configuration (console device login) is valid.
- **Catalog fetch credential fallback:** the model-catalog fetch used the first
  configured API key as its bearer. With no API keys set it now falls back to
  the first plugin-owned OAuth console credential's access token, so the
  catalog still refreshes. A configured API key always wins when present.
- **Upgrade notes:** existing `api-keys` configurations are unchanged; this is
  additive. Deployments that drop the opencode-go API keys from the EasyCLI
  config and `.env` and rely on the OAuth login now work instead of failing
  config validation.

## v2.4.0 — 2026-11-10 (free-tier session fingerprint + OAuth console login)

Builds on the re-derived v2.3.0 deployed surface (below) without changing its
wire ABI or config contract. This release lets the opencode.ai free-tier models
served through the gateway clear the free-tier gate, adds OAuth device-code
console login as a second credential type, and reads the catalog limits from
OpenCode's native envelope.

- **Free-tier session fingerprint:** opencode.ai gates its free models on the
  request looking like a genuine OpenCode session. For records routed to the free
  `/inference/` plane, the plugin now shapes the upstream body (harness preamble
  including the Code Mode section and the `<env>` block, a non-empty tools array,
  and `stream:true`), sends the OpenCode client-identity header set, uses the
  client-form User-Agent, and generates the session id in the exact OpenCode format
  (`ses_` + 12 hex descending-timestamp chars + 14 base62 random chars). An explicit
  inbound session header is honored verbatim over the namespaced host metadata; a
  non-stream request that forces `stream:true` upstream has its SSE response
  collapsed back into a single chat-completions document. Paid plans are untouched.
  Free-tier membership is decided by the route (the plan base URL), not the model id
  suffix, so suffix-less free models (big-pickle) are shaped too.
- **OAuth device-code console login:** a second, plugin-owned credential type
  (`opencode-go-oauth-<sha256>.json`) signs in through the console device-code flow
  and routes org-scoped inference with `x-opencode-org-id` + the client User-Agent.
  Reconciliation never touches OAuth records; a credential is either an API key or
  an OAuth token (routed on the `credential_type` attribute), and the executor sends
  whichever as the upstream bearer.
- **Catalog limits from the native envelope:** context/output limits are read from
  OpenCode's `{"context":N,"output":M}` envelope instead of the flat fields it does
  not emit, so catalog records carry real limits.
- **Identity rename:** the runtime plugin name/id is now `easycliproxyapi-opencode-provider`,
  matching the library basename and the sibling provider plugins' convention
  (`easycliproxyapi-{commandcode,mistral,chatgpt}-provider`). Previously the panel
  displayed `opencode-go-provider` while the `.so` and the EasyCLI config key were
  already `easycliproxyapi-opencode-provider`, so the tile read inconsistently next to
  the other plugins. Host binding is by the `.so` basename (unchanged), so the EasyCLI
  config key and model prefix (`opencode-go`) are untouched; only the reported name
  changed.
- **CI:** same-repository PR checks route to the dedicated rootless org runner.

### Upgrade notes

- No config migration: the `plans:`/`api-keys` schema is unchanged. A new free plan
  (`base-url` on the `/inference/` plane) is opt-in; until one is declared, only the
  paid plans are routable, exactly as in v2.3.0.
- Free models on the `/inference/` plane additionally need a `route-overrides` entry
  (chat-completions) per model in the EasyCLI config to be routable.
- The wire ABI, registration surface and artifact filename are unchanged; the
  reported version moves 2.3.0 -> 2.4.0.

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
- **Identity:** runtime plugin id stays `easycliproxyapi-opencode-provider` (the EasyCLI
  config-contract key pinned by `simple-central-proxy-manager/easycli/tests`);
  the shared library is published as `easycliproxyapi-opencode-provider` to
  match the deployed artifact. Version is `2.3.0`; `registry.json` and the
  repository URL point at this repository.
- **CI load smoke:** `.github/workflows/tests.yml` builds the glibc
  `linux/amd64` artifact before `go test`, and `internal/loadsmoke`
  `dlopen()`s it, calls `cliproxy_plugin_init` with a fake host ABI and
  replays the identity, credential-reconcile and no-leak checks through the
  real C exports. It skips (with a printed reason) when the artifact cannot
  be built in that environment.
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
