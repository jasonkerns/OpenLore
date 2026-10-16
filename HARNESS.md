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
| `openlore harness init` / `setup-code`: first-run setup, the starter folder layout and setup codes (below) | `cmd/openlore/harness_cmd.go`, `harness_layout.go` |
| The `/harness` API: people, agent activity, passkey links, health check, the change feed, file versions and compare-and-swap editing (below) | `pkg/openlore/harness_admin.go`, `harness_changes.go` |
| Identity names may contain `@` (people are named by email); `/` stays reserved | `internal/config/config.go` |
| `X-Harness-Agent` / `X-Harness-Agent-Id` on a request are recorded as `Attribution.Agent`/`AgentID`: a label, never authority, shown in history as `email:agent` (`Attribution.Label`), over MCP, `/api` and `/harness` (the app labels its own writes for an agent, such as copied OpenSpec documents); `String` is unchanged because folder rules and session binding match on it | `pkg/openlore/middleware.go`, `auth_middleware.go`, `harness_admin.go`, `history.go` |
| `openlore token mint --actor <identity>` mints a delegated token (the actor must be one of the principal's `delegates`) | `cmd/openlore/main.go` |
| `openlore tls cert --cert … --key … --hosts …` writes a self-signed certificate and prints its SHA-256 fingerprint, for HTTPS on a private network where clients pin the certificate | `cmd/openlore/tls_cmd.go` |

The library keeps upstream's defaults (keyless, `unknown_identity: allow`) so upstream's tests and merges stay simple;
the `openlore` binary is where the rules are enforced.

## How Agent Harness uses it

- **Identities.** Each person is an identity named by their email, accountable for everything done with their
  tokens: their own edits and their agents' writes. Admins have the `owner` role (`lore:config:edit`,
  `lore:access:view`, read/write on all docs); everyone else the `member` role (read/write docs). `harness init`
  also creates a bootstrap identity (`--owner`, default `owner`) used only by setup codes. Agents aren't identities:
  the app's relay labels each request with the agent's name and id (`X-Harness-Agent*`), recorded with each change.
- **First start.** `openlore harness init --dir <dir> --url https://<public address> [--owner <name>]` writes
  `openlore.yml` (SSH off, plain HTTP on 8080, tokens and passkeys for the public address, Agent Skills on) and
  `lore.json`, and makes the `docs` folder. Existing files are kept, so it's safe on every start (`deploy/aws/start.sh`
  runs it). The first time, it prints a **setup code** (`olsetup1.` + the address and a one-hour owner token).
- **Starter layout.** An empty `docs` folder gets one server's worth of folders for all projects, organised around
  OpenSpec (`cmd/openlore/harness_layout.go`): `projects/<project>/` (`project.md`, and `specs/`, `changes/`,
  `archive/` copied from the repo's `openspec/`), `standards/`, `ideas/`, `explorations/`, `decisions/`, `runbooks/`
  and `skills/`, each with a short `README.md` saying what goes there (except `skills/`, where every child must be a
  skill; it becomes a collection with `skills enable /skills`, which the app runs; that marker is an extended attribute,
  so on storage without them (Windows, probably EFS) it answers `unsupported` and the app copies the skills without it).
  The config allows scripts (`.sh`, `.ps1`, `.py`, …) besides upstream's default file types, since skills carry them. Agent Harness will give each kind of folder its own menu, by path. Plan:
  `docs/openspec-lore-plan.md` in the harness.
- **Joining.** Pasting the setup code into the app's Settings → Knowledge base, with your email, makes you an admin:
  the app calls `POST /harness/people` with the bootstrap token (or `…/code` if you're already there) and keeps only
  your own one-year token. Admins add people (each gets a **connection code**, `olpair1.` + address, token and
  email), give someone another code (another computer; their other codes keep working), and remove people, which
  deletes the identity and revokes all their tokens at once. For a fresh setup code later:
  `openlore harness setup-code --config <dir>/openlore.yml`.
- **Agents** reach `/mcp` through a relay in the app, each at its own address, that adds the person's token and the
  agent label; they never see the token.

### The `/harness` API

Bearer tokens only (no cookies, so no CSRF). "Admin" means the caller has `lore:config:edit`.

| Route | Who | |
|---|---|---|
| `GET /harness/health` | anyone | `ok`, for load balancer health checks |
| `GET /harness/me` | any identity | `{principal, actor, admin, passkeys}` |
| `GET /harness/activity?agent_id=…&cursor=` | any identity | the caller's own changes made by that agent, newest first |
| `GET /harness/people` | admin | identities named by email, with whether each is an admin |
| `POST /harness/people` `{email, admin}` | admin | adds the person (`owner` or `member` role); returns their token (409 if they exist; needs the `member` role in lore.json, which `harness init` writes) |
| `POST /harness/people/{email}/code` | admin | another token for an existing person |
| `DELETE /harness/people/{email}` | admin | removes them, revoking every token (409 for yourself) |
| `POST /harness/passkey?device=…` | admin | a one-time, 5-minute link that registers a passkey for the caller, for the dashboard |
| `GET /harness/changes?cursor=&limit=` | any identity | every file change the caller can read, newest first (from the commit journal): who (`by` = `email:agent`), when, and per file the action and before/after hashes |
| `GET /harness/blob?path=&hash=` | any identity | one version of a file, only if its history shows that hash for that path (new bytes from the change itself, earlier ones from the pre-image store, else the current file) |
| `GET /harness/file?path=` | any identity | a document's text and hash, the base for an edit |
| `PUT /harness/file?path=` | anyone who may write there | saves the body; needs `If-Match: <hash>` (replace that version) or `If-None-Match: *` (create; parent folders are made); 412 with the current hash if it changed; goes through the write log, so history, rules and attribution apply |
| `DELETE /harness/file?path=` | anyone who may write there | removes the document if it's still `If-Match` |

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
