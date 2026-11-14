## What's Changed

### v2.5.1 — quota buckets named after their window

`easycliproxyapi-opencode-provider` v2.5.1 fixes the native quota-card
bucket names. Every bucket previously carried the window's current STATUS
(`ok` / `exceeded`) in its `window` field, so all three buckets read `ok`
and quota consumers could not tell the rolling, weekly and monthly limits
apart. No wire ABI, registration-surface, config or artifact-name change.

- **Window identity, not state.** Buckets are now named after the window
  they meter: `rolling`, `weekly`, `monthly`. The window's state continues
  to ride `remaining_fraction` (and `reset_time`), exactly as before.

### Upgrade notes

- Drop-in `.so` replacement; restart the gateway container. Existing
  configurations and credentials are untouched.
- Quota consumers that grouped buckets by the `window` field now see three
  distinct windows instead of one repeated `ok` label.
- The reported version moves `2.5.0` -> `2.5.1`.
