# Agent Harness fork

This branch (`agent-harness`) is the OpenLore server that Agent Harness bundles as its shared knowledge base. It
differs from upstream in one way that matters: **there is no anonymous access.** Every request must come from a
known identity.

## Changes from upstream

| Change | Where |
|---|---|
| The server refuses to start if any way in would be anonymous: no `auth_file`, `allow_keyless: true`, `unknown_identity` other than `deny`, `mcp.require_auth: false` (or missing tokens) with MCP/the API on, or a docset or identity that uses the `guest` role | `cmd/openlore/locked.go` |
| "Continue with public access" is gone from the OAuth login page, along with `POST /authorize/public` | `pkg/openlore/authorize.go`, `oauth_metadata.go` |
| Tokens for the reserved `anonymous` subject, and tokens whose subject matches no identity, are refused instead of becoming guest | `pkg/openlore/identitystore.go` |
| `openlore mcp` and `openlore mcpb` (stdio, no login) are disabled | `cmd/openlore/main.go` |
| `port: 0` turns SSH off; the server then runs HTTP(S) only | `internal/config/config.go`, `pkg/openlore/server.go`, `internal/httpserver` |
| `lore.json` is re-read when it changes on disk (every 2 s), so adding or removing an identity or delegate takes effect without a restart | `pkg/openlore/auth_watch.go` |
| `openlore token mint --actor <identity>` mints a delegated token (the actor must be one of the principal's `delegates`) | `cmd/openlore/main.go` |
| `openlore tls cert --cert … --key … --hosts …` writes a self-signed certificate and prints its SHA-256 fingerprint, for HTTPS on a private network where clients pin the certificate | `cmd/openlore/tls_cmd.go` |

The library keeps upstream's defaults (keyless, `unknown_identity: allow`) so upstream's tests and merges stay simple;
the `openlore` binary is where the rules are enforced.

## How Agent Harness uses it

The harness writes `openlore.yml` and `lore.json` under its data folder and runs
`openlore -c openlore.yml <docs>` with SSH off and a self-signed certificate. The user is the one identity with the
`owner` role; each machine is an identity named `machine-<name>` that is a delegate of the user, with
`lore:config:edit` denied. Agents reach the server through a relay in the harness that adds the machine's token and
pins the certificate. Removing a machine deletes its identity, which revokes its token.

## Tests

`go test ./...`. On Windows about 120 upstream tests fail on `main` as well (Unix file modes, xattrs, path handling);
compare against `main` rather than expecting a clean run. The tests for these changes are in
`cmd/openlore/locked_test.go`, `pkg/openlore/authorize_test.go` (`TestAuthorize_NoPublicAccess`),
`auth_middleware_test.go` (`TestAuthMiddleware_AnonymousTokenIsRefused`) and `internal/config/documented_keys_test.go`.

## Updating from upstream

```sh
git remote add upstream https://github.com/aakarim/OpenLore.git   # once
git fetch upstream
git checkout agent-harness
git merge upstream/main
```
