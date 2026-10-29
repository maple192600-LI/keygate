# Historical migration fixtures

These unchanged public `TEST_ONLY` files come from baseline commit
`07499ee6391c911184d412fbf32442a31e086772`. They let the actor-history test
restore a valid 2026-10-23 MachineGuid-era row, then run every migration through
the current schema while checking that the stored request and signed-license
bytes, hash, and creator remain unchanged. They are not accepted by the current
runtime parser and do not add a production compatibility path.

| File | SHA-256 |
| --- | --- |
| `TEST_ONLY.license-v2.snaillic` | `f00cf2cac79761b04c6ce0df466cd54d7f0826662dd4db8cf47a81b173be05e1` |
| `TEST_ONLY.request-v2.snailreq` | `7ced1e9cbaed80ae0237fa658be64afe32c2ae3967e7f4ecef47d45d6653529d` |
