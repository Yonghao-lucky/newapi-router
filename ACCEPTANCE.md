# Acceptance

## Results (2026-10-10 UTC)

- Backend: full `go test -p 2 -count=1 ./service/ai_router ./relay ./middleware ./controller ./router` passed. After the final model-authorization precedence guard, strict ingress HTTP regression and all service/relay/middleware/router tests passed again.
- `cd relaykit && GOWORK=off go build -p 2 ./...` passed; root CGO_ENABLED=0 binary build passed with Go 1.26 container (same project build generation).
- Frontend: typecheck and affected-file lint passed. AI Router suites: 29 tests passed. Ordinary channel configuration component: 71 tests passed. Ordinary channel settings roundtrip: 3 tests passed after correcting the new test fixture's missing required metadata. All five switches tested off/on/save/reopen independently.
- The silent policy-save failure was first reproduced with a failing UI test on baseline, then passed with the fix. Validation network failure is also visible and leaves buttons usable.
- Seven locale files updated through the prescribed script; `bun run i18n:sync` and `bun run build` passed. Expected jsdom canvas/reduced-motion warnings did not fail tests.
- Database roundtrip: real SQLite 3.50.4, MySQL 8.0.46, PostgreSQL 15.19. `TestAIRouterChannelManagementDatabaseMatrix` passed twice (`-count=2`) using dedicated `compat_v2_matrix` databases. Initial post-bootstrap rerun conflicted with the test application's existing policy; moving matrix fixtures into their own databases resolved this fixture collision. Production databases never used for test writes. No schema migration or database dependency change introduced.
- Isolated HTTP mock acceptance: 16 cases passed, including streaming/nonstreaming typed errors and thinking modes. Final replay additionally verifies rejected requests never reach mock upstream and exact outgoing four-parameter values.

## Resource 48 live acceptance

Production resource 48 was read with SELECT only; only necessary provider connection fields were copied to the isolated test database. No production channel, token, group, option or policy was changed. Concurrency 1; 18 short requests total.

| Request | Nonstream | Stream | Reasoning |
|---|---|---|---|
| thinking.enabled | 3/3 HTTP200 | 3/3 HTTP200 with DONE | present; 24–53 reported reasoning tokens |
| thinking.disabled | 3/3 HTTP200 | 3/3 HTTP200 with DONE | absent; reasoning_tokens=0 |
| thinking.disabled + reasoning_effort=high | 3/3 HTTP200 | 3/3 HTTP200 with DONE | absent; reasoning_tokens=0 |

No response filter or usage correction was implemented. Both disabled and high are kept, verified independently with outbound mock capture. Live results are in the private test runtime `real-results.json` (counts/usage only, no model reasoning text or credentials).

Live matrix ran on candidate image sha256:7e93be522c5a3c2488e8000641b8965439387293a2ae4d431c273f6e4ec2f74d. Subsequent final changes consolidated numeric serialization/preserved equivalent numeric spellings and preserved existing forbidden-model error precedence; covered by final regressions and final-image mock replay. No additional paid live requests were used for those changes.

## Limitations / release boundary

- This is scoped functional/regression verification, not a performance certification, all-provider capability guarantee, penetration test or full-project test run.
- Thinking closure requires a verified upstream and exact mapped-model compatibility scope. For non-strict routes with no opt-in capability, legacy handling remains in place. Unavailable capability metadata fails closed with a 503 rather than claiming unsupported input.
- Tests use current supported database versions, not every historical minimum version. No dialect-specific new schema feature is used.
- Candidate only: no production deployment, GitHub push, global Docker cleanup, proxy/firewall changes or production configuration writes.
