## What's Changed

### v2.5.3 — duration-style window labels

`easycliproxyapi-opencode-provider` v2.5.3 labels the native quota-card
buckets with the shared duration convention used by the native Codex
cards. No wire ABI, registration-surface, config or artifact-name change.

- **Window labels.** The rolling window is now `5h`, the weekly window
  `7d` and the monthly window `30d` (previously `rolling`, `weekly`,
  `monthly`). The underlying windows, values and reset times are
  unchanged.

### Upgrade notes

- Drop-in `.so` replacement; restart the gateway container.
- Quota consumers that key buckets by the `window` field see the new
  duration labels.
- The reported version moves `2.5.2` -> `2.5.3`.
