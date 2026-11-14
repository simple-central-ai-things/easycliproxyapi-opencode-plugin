## What's Changed

### v2.5.2 — OAuth credential naming: opencode-oauth-

`easycliproxyapi-opencode-provider` v2.5.2 renames the plugin-owned OAuth
console credential files. The provider is `opencode-go`, but the credential
is an OpenCode OAuth login — not a "go" variant — so the file naming drops
the misleading `go` segment. No wire ABI, registration-surface, config or
artifact-name change.

- **New file name.** OAuth console logins now write
  `opencode-oauth-<sha256>.json` (previously
  `opencode-go-oauth-<sha256>.json`). The hash input is unchanged (account
  id, else email, else refresh token), so a re-login for the same account
  still targets one record.

- **Legacy records keep working.** Credential detection accepts both the
  new and the legacy prefix, so an existing `opencode-go-oauth-*` record
  continues to serve and refresh until it is migrated or re-logged-in.

### Upgrade notes

- Drop-in `.so` replacement; restart the gateway container. Existing
  OAuth credentials keep working without any migration.
- Optional migration: rename the auth file (and its internal `id` field)
  from `opencode-go-oauth-<sha>.json` to `opencode-oauth-<sha>.json`.
  Consumers that bind the credential by its management `auth_index`
  (e.g. SCPM's provider-quota config) must re-resolve the index after a
  rename, because the index derives from the record id.
- The reported version moves `2.5.1` -> `2.5.2`.
