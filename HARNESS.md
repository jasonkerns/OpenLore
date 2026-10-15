# Agent Harness fork

This branch (`agent-harness`) is the OpenLore server that Agent Harness connects to as its shared knowledge base. It
runs on its own (meant for AWS, behind a load balancer that handles HTTPS); the app only connects to it. It differs
from upstream in one way that matters: **there is no anonymous access.** Every request must come from a known
identity.

## Changes from upstream

| Change | Where |
|---|---|
| The server refuses to start if any way in would be anonymous: no `auth_file`, `allow_keyless: true`, `unknown_identity` other than `deny`, `mcp.require_auth: false` (or missing tokens) with MCP/the API on, or a docset or identity that uses the `guest` role | `cmd/openlore/locked.go` |
| "Continue with public access" is gone from the OAuth login page, along with `POST /authorize/public` | `pkg/openlore/authorize.go`, `oauth_metadata.go` |
| Tokens for the reserved `anonymous` subject, and tokens whose subject matches no identity, are refused instead of becoming guest | `pkg/openlore/identitystore.go` |
| `openlore mcp` and `openlore mcpb` (stdio, no login) are disabled | `cmd/openlore/main.go` |
| The shell's `passkey` command is an admin action (needs `lore:config:edit`). Upstream treats it as a read, so any signed-in caller could register a passkey for any identity, including the owner | `internal/passkeys/command.go` |
| `port: 0` turns SSH off; the server then runs HTTP(S) only | `internal/config/config.go`, `pkg/openlore/server.go`, `internal/httpserver` |
| `lore.json` is re-read when it changes on disk (every 2 s), so adding or removing an identity or delegate takes effect without a restart | `pkg/openlore/auth_watch.go` |
| `openlore harness init` / `setup-code`: first-run setup and setup codes (below) | `cmd/openlore/harness_cmd.go` |
| The `/harness` API: machines, passkey links, health check (below) | `pkg/openlore/harness_admin.go` |
| `openlore token mint --actor <identity>` mints a delegated token (the actor must be one of the principal's `delegates`) | `cmd/openlore/main.go` |
| `openlore tls cert --cert … --key … --hosts …` writes a self-signed certificate and prints its SHA-256 fingerprint, for HTTPS on a private network where clients pin the certificate | `cmd/openlore/tls_cmd.go` |

The library keeps upstream's defaults (keyless, `unknown_identity: allow`) so upstream's tests and merges stay simple;
the `openlore` binary is where the rules are enforced.

## How Agent Harness uses it

- **Identities.** The owner is the one identity with the `owner` role (`lore:config:edit`, `lore:access:view`, read/write
  on all docs). Each computer running Agent Harness is an identity `machine-<name>` that is a delegate of the owner,
  so its writes are recorded as the owner acting through that machine. Ordinary machines have `lore:config:edit`
  denied; admin machines don't.
- **First start.** `openlore harness init --dir <dir> --url https://<public address> [--owner <name>]` writes
  `openlore.yml` (SSH off, plain HTTP on 8080, tokens and passkeys for the public address) and `lore.json`, and makes an
  empty `docs` folder. Existing files are kept, so it's safe on every start (`deploy/aws/start.sh` runs it). The first
  time, it prints a **setup code** (`olsetup1.` + the address and a one-hour owner token).
- **Joining.** Pasting the setup code into the app's Settings → Knowledge base makes that computer an admin machine:
  the app calls `POST /harness/machines` with the owner token and keeps only the machine's own one-year token. An
  admin machine adds others (each gets a **connection code**, `olpair1.` + address, token and machine name) and
  removes them, which deletes the identity and revokes its token at once. For a fresh setup code later:
  `openlore harness setup-code --config <dir>/openlore.yml`.
- **Agents** reach `/mcp` through a relay in the app that adds the machine's token; they never see it.

### The `/harness` API

Bearer tokens only (no cookies, so no CSRF). "Admin" means the caller has `lore:config:edit`.

| Route | Who | |
|---|---|---|
| `GET /harness/health` | anyone | `ok`, for load balancer health checks |
| `GET /harness/me` | any identity | `{principal, actor, admin, passkeys}` |
| `GET /harness/machines` | admin | the owner's `machine-*` delegates |
| `POST /harness/machines` `{name, admin}` | admin | adds `machine-<name>`; returns its token (409 if it exists) |
| `DELETE /harness/machines/{name}` | admin | removes it (409 for the caller's own machine) |
| `POST /harness/passkey?device=…` | admin | a one-time, 5-minute link that registers a passkey for the owner, for the dashboard |

## Development

`node scripts/harness-dev.mjs [--seed <docs folder>]` runs the server on http://localhost:8485 from `bin/openlore.exe`
(build with `go build -o bin/openlore.exe ./cmd/openlore`; for the dashboard to be embedded, first
`npm --prefix dashboard ci && npm --prefix dashboard run build`), set up with `harness init` in `.dev/` (untracked), and
the dashboard's Vite dev server with hot reload on http://localhost:5180/dashboard/, signed in as the owner through
the proxy. The first run prints a setup code for connecting a local Agent Harness.

## Deploying

`deploy/aws/`: `Dockerfile` (builds the dashboard and the server; runs as uid 1000 with `/data` as the volume),
`start.sh` (the entrypoint: `harness init`, then serve; needs `OPENLORE_URL`, optional `OPENLORE_OWNER`) and
`buildspec.yml` (CodeBuild: build and push to ECR). Plan for ECS Fargate: one task (state is files, so never more than
one), an EFS volume at `/data` (access point uid/gid 1000), an ALB with an ACM certificate for the public name
(Route 53) forwarding to port 8080, health check `/harness/health`. The first start's setup code appears in the
task's log.

## Tests

`go test ./...`. On Windows about 120 upstream tests fail on `main` as well (Unix file modes, xattrs, path handling,
directory sync); compare against `main` rather than expecting a clean run. The tests for these changes are in
`cmd/openlore/locked_test.go`, `cmd/openlore/harness_cmd_test.go`, `pkg/openlore/harness_admin_test.go`,
`internal/passkeys/harness_test.go`, `pkg/openlore/authorize_test.go` (`TestAuthorize_NoPublicAccess`),
`auth_middleware_test.go` (`TestAuthMiddleware_AnonymousTokenIsRefused`) and `internal/config/documented_keys_test.go`.

## Updating from upstream

```sh
git remote add upstream https://github.com/aakarim/OpenLore.git   # once
git fetch upstream
git checkout agent-harness
git merge upstream/main
```
