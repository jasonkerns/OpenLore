package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
)

const lockedYAML = `version: "1"
port: 0
http_port: 18080
auth_file: ./lore.json
mcp:
  require_auth: true
tokens:
  issuer: https://localhost:18080
  audience: https://localhost:18080
`

const lockedAuth = `{
  "allow_keyless": false,
  "unknown_identity": "deny",
  "docsets": { "docs": { "paths": ["/"], "access": { "allow": { "owner": "rw" } } } },
  "roles": { "owner": {} },
  "identities": [ { "name": "jason", "roles": ["owner"] } ]
}`

func lockedConfig(t *testing.T, yaml, auth string) config.Config {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "openlore.yml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		if err := os.WriteFile(filepath.Join(dir, "lore.json"), []byte(auth), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.New(config.WithConfigFile(filepath.Join(dir, "openlore.yml")))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthFile != "" && !filepath.IsAbs(cfg.AuthFile) {
		cfg.AuthFile = filepath.Join(dir, cfg.AuthFile)
	}
	return cfg
}

func TestRequireNoAnonymousAcceptsLockedConfig(t *testing.T) {
	if err := requireNoAnonymous(lockedConfig(t, lockedYAML, lockedAuth)); err != nil {
		t.Fatalf("locked config rejected: %v", err)
	}
}

func TestRequireNoAnonymousRejectsOpenings(t *testing.T) {
	cases := []struct {
		name, yaml, auth, want string
	}{
		{"no auth file", strings.Replace(lockedYAML, "auth_file: ./lore.json\n", "", 1), "", "auth_file is required"},
		{"keyless ssh", lockedYAML, strings.Replace(lockedAuth, `"allow_keyless": false`, `"allow_keyless": true`, 1), "allow_keyless"},
		{"keyless by default", lockedYAML, strings.Replace(lockedAuth, `"allow_keyless": false,`, "", 1), "allow_keyless"},
		{"unknown identities allowed", lockedYAML, strings.Replace(lockedAuth, `"deny"`, `"allow"`, 1), "unknown_identity"},
		{"mcp without token", strings.Replace(lockedYAML, "require_auth: true", "require_auth: false", 1), lockedAuth, "require_auth"},
		{"guest docset grant", lockedYAML, strings.Replace(lockedAuth, `"owner": "rw"`, `"owner": "rw", "guest": "ro"`, 1), "guest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireNoAnonymous(lockedConfig(t, tc.yaml, tc.auth))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}
