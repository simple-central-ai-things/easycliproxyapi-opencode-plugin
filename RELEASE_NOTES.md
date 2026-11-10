## What's Changed

### v2.5.0 — OAuth can be the sole credential source

`easycliproxyapi-opencode-provider` v2.5.0 makes `api-keys` optional so an
opencode-go provider configured with only an OAuth console login (no API keys)
loads and serves models. It builds on v2.4.0 (free-tier session fingerprint +
OAuth console login) without changing the wire ABI, registration surface, or
the `easycliproxyapi-opencode-provider.so` artifact name.

- **Optional `api-keys`.** The config no longer rejects an empty `api-keys`
  list. An OAuth-only configuration — one that signs in through the console
  device-code flow and stores no API keys — is now valid and serves models.

- **Catalog fetch credential fallback.** The model-catalog fetch (`/models`)
  previously used the first configured API key as its bearer. When no API keys
  are set, it now falls back to the first plugin-owned OAuth console
  credential's access token, so the catalog still refreshes. A configured API
  key always wins when present.

### Upgrade notes

- Existing `api-keys` configurations are unchanged; this release is additive.
- To drop the opencode-go API keys from a deployment: remove them from the
  EasyCLI `config.yaml` `api-keys` list and the deploy `.env`, keep the OAuth
  console login, and restart. The provider then runs on the OAuth credential
  alone.
- The reported version moves `2.4.0` -> `2.5.0`.
