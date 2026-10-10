# K3 request compatibility v2

Status: implementation in progress; not approved for production deployment.

## Hard boundaries

- Server: 184.164.79.210. Production baseline: 4318a0cadd8999fe45bba508a6696ba310e4db74; image new-api-video-router:4318a0cad, ID sha256:2c09ecbe5754c1fb522500ee8cac8d501d448f764fe135e9c4e44fb82fe7ddcf.
- Worktree: /root/new-api-router-worktrees/k3-request-compat-v2; branch feat/k3-request-compat-v2.
- Runtime: /root/new-api-router-k3-compat-v2-test-runtime; test port 127.0.0.1:13184 (checked unused before creation).
- New containers/network use new-api-router-k3-compat-v2-test-* names, their own databases, Redis and directories. No production mounts, DSNs, Redis, restarts, configuration writes, image replacement, cleanup or proxy/firewall changes.
- Resource limits: builds at most 4 CPUs/8 GiB; test app 2 CPUs/2 GiB; database containers 1 CPU/1 GiB each. Real provider tests serial, short prompts, maximum 18 thinking matrix requests before investigation.
- Production channel 48 is read-only resource provenance for real acceptance. Its credentials must not appear in source, reports, command lines or logs. Do not enable/change the production channel.
- Local /tmp/k3-request-compat-v2-stage is only an editing mirror; synchronize explicit changed files into the remote independent worktree. Existing local worktree changes belong to the user.

## Scope

1. Four independent default-off channel flags normalize numeric temperature=1, top_p=.95, presence_penalty=0, frequency_penalty=0 before existing K3 fixed-value validation. Preserve missing/null, reject nonnumeric types, preserve original request and retry isolation.
2. Default-off channel flag allows thinking.disabled through strict ingress with capability-aware selection/fallback. Preserve disabled AND reasoning_effort=high. Do not suppress reasoning or synthesize/modify usage. Validate real behavior on resource 48.
3. Field-specific content errors; deterministic mixed-content classification; distinguish unsupported input (400) from unavailable channels (503), preserve unknown fallback, original upstream errors and SSE header contracts.
4. Policy management: visible persistent save/validation errors, preserve dirty values, fix validate endpoint, return all findings, do not automatically delete/enable channels.

## Verification / handoff

- Extend existing focused tests; mock outbound parameter capture, scalar boundaries, off/on combinations, retry isolation, thinking modes, errors and policy UI failures.
- Backend targeted tests/build; relaykit independently built; frontend tests/typecheck/lint/build and seven locale translations via prescribed script.
- Real isolated SQLite/MySQL/PostgreSQL settings roundtrip and policy operations. No production DB test writes.
- Real channel 48 thinking matrix: enabled, disabled, disabled+high; stream/nonstream; three repetitions. Report actual reasoning presence and upstream token details; missing data is not zero.
- Acceptance report and handoff must distinguish passed, failed, not run. Candidate image only; no GitHub push/deployment without separately settled authorization.
