# Handoff

## Location

- Server: 184.164.79.210.
- Branch: feat/k3-request-compat-v2; base 4318a0cadd8999fe45bba508a6696ba310e4db74.
- Source: /root/new-api-router-worktrees/k3-request-compat-v2.
- Runtime: /root/new-api-router-k3-compat-v2-test-runtime.
- Test app: new-api-router-k3-compat-v2-test-app; localhost only, 127.0.0.1:13184.
- Independent PostgreSQL/Redis/MySQL/mock/network, names prefixed new-api-router-k3-compat-v2-test. App DB compat_v2, database matrix DB compat_v2_matrix. No production data volumes.
- Frontend and backend build/test logs, redacted mock/live results, Dockerfile, compose.yml, and revision-image manifest are stored in the runtime.
- Test administrator: compatadmin. Random test credentials are only in runtime/fixture-credentials.json (0600 under a 0700 directory); not committed or printed.

## How to inspect

SSH tunnel: `ssh -L 13184:127.0.0.1:13184 root@184.164.79.210`, then open http://127.0.0.1:13184.

AI Router -> Channel Config -> Configure -> existing Chat compatibility section, set exact outgoing model names (e.g. kimi-k3), then independently choose:

- K3 temperature compatibility (numeric -> 1)
- K3 top_p compatibility (numeric -> 0.95)
- K3 presence_penalty compatibility (numeric -> 0)
- K3 frequency_penalty compatibility (numeric -> 0)
- Allow requests to disable thinking (enabled/disabled and high preserved; no usage rewriting)

All new persisted flags default false. Normal channel editor retains the flags when editing other settings. AI Router compatibility updates preserve fields omitted by older clients. Existing settings/priority/billing behavior are not intentionally changed.

Policy editor now displays business and network errors inline and retains dirty input. Validate endpoint supports both current and legacy paths. Candidate problems remain explicit; no automatic removal/re-enabling. HTTP400 input diagnostics retain field paths; capacity shortage is HTTP503.

## Security / scope

Authentication implementation, session settings and token handling were not replaced. Existing authentication and forbidden-model responses retain precedence over new diagnostics, verified by strict ingress integration tests. Applied OWASP Authorization Cheat Sheet principles (server-side per-request checks, safe failure, no sensitive public error values); reviewed Authentication and Session Management Cheat Sheet guidance for preserving the existing boundaries. No broad ASVS-compliance claim is made.

## Deployment is NOT authorized in this task

Production 13000 remains new-api-video-router:4318a0cad / sha256:2c09ecbe5754c1fb522500ee8cac8d501d448f764fe135e9c4e44fb82fe7ddcf. Production app container ID 5f81647b15037f4b7b95d0efacbeb02090bf091175d3122a45611bff25e14dcc, started 2026-10-08T11:28:02Z. 3000 and 13082 also not restarted. Existing images preserved.

GitHub read-only check: feat/k3-chat-request-validation=4318a0cad; main=19fad0cd2. Developed from verified runtime branch, not main. No GitHub push performed.

Before any later deployment, recheck production drift, review ACCEPTANCE.md, capture current image/config rollback metadata, obtain explicit deployment approval, and replace only the intended app. Never apply the isolated test Compose to production.
