## What's Changed

### v2.4.0 — free-tier session fingerprint + OAuth console login

`easycliproxyapi-opencode-provider` v2.4.0 builds on the re-derived v2.3.0
surface without changing its wire ABI, registration metadata, artifact filename
or `plans:`/`api-keys` config contract. It adds two credential/capability
areas and one catalog fix. See `CHANGELOG.md` and `PROVENANCE.md`.

- **opencode.ai free-tier models through the gateway.** opencode.ai gates its
  free models on the request looking like a genuine OpenCode session. For
  records routed to the free `/inference/` plane, the plugin now:
  - shapes the upstream body into the genuine-OpenCode session fingerprint
    (harness preamble including the `# Code Mode` section and the `<env>`
    environment-context block, a non-empty `tools` array, and `stream:true`);
  - sends the OpenCode client-identity header set and the client-form
    User-Agent;
  - generates the session id in the exact OpenCode client format —
    `ses_` + 12 lowercase-hex descending-timestamp chars + 14 base62 random
    chars — so a session id the gate would otherwise reject is never sent;
  - honors an explicit inbound session header verbatim (over the host's
    namespaced session metadata), and collapses the forced-upstream `stream:true`
    SSE response back into a single chat-completions document for non-stream
    callers.
  Free-tier membership is decided by the **route** (the plan base URL on the
  `/inference/` plane), not the model-id suffix, so suffix-less free models are
  shaped too. Paid plans are untouched.

- **OAuth device-code console login** as a second, plugin-owned credential
  type (`opencode-go-oauth-<sha256>.json`). It signs in through the console
  device-code flow and routes org-scoped inference with `x-opencode-org-id`
  plus the client User-Agent. Reconciliation never touches OAuth records — a
  credential is either an API key or an OAuth token (routed on the
  `credential_type` attribute), and the executor sends whichever as the
  upstream bearer.

- **Catalog limits from OpenCode's native envelope.** Context/output limits are
  read from the `{"context":N,"output":M}` envelope instead of flat fields
  OpenCode does not emit, so catalog records carry real limits.

### Notes

- No config migration: the `plans:`/`api-keys` schema is unchanged. Declaring a
  free plan (`base-url` on the `/inference/` plane) is opt-in; until one is
  declared, only the paid plans are routable, exactly as in v2.3.0.
- Free models on the `/inference/` plane additionally need a per-model
  `route-overrides` entry (chat-completions) in the EasyCLI config to be
  routable.
- The reported version moves `2.3.0` -> `2.4.0`; the wire ABI, registration
  surface and the `easycliproxyapi-opencode-provider.so` artifact name are
  unchanged.
