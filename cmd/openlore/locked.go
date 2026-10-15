package main

import (
	"errors"
	"fmt"
	"slices"

	"github.com/aakarim/go-openlore/internal/config"
)

// requireNoAnonymous refuses to serve unless every way in requires a login.
// This build (the Agent Harness fork) has no anonymous access: a missing auth
// file would otherwise give everyone read/write, keyless SSH or unknown keys and
// tokens would land in the guest role, and MCP/the JSON API could run without a
// token. Each of these is a config mistake the server now rejects at startup
// rather than a posture it serves.
func requireNoAnonymous(cfg config.Config) error {
	if cfg.AuthFile == "" {
		return errors.New("auth_file is required: without lore.json every caller would get read/write access")
	}
	auth, err := config.LoadAuthConfig(cfg.AuthFile)
	if err != nil {
		return fmt.Errorf("loading %s: %w", cfg.AuthFile, err)
	}
	keyless := cfg.AllowKeyless
	if auth.AllowKeyless != nil {
		keyless = *auth.AllowKeyless
	}
	if keyless {
		return errors.New(`allow_keyless must be false: keyless SSH is anonymous access`)
	}
	unknown := cfg.UnknownIdentity
	if auth.UnknownIdentity != "" {
		unknown = auth.UnknownIdentity
	}
	if unknown != "deny" {
		return errors.New(`unknown_identity must be "deny": unknown keys and tokens would get guest access`)
	}
	if (cfg.MCPEnabled || cfg.APIEnabled) && cfg.HTTPPort > 0 {
		if !cfg.HTTPAuthRequired() {
			return errors.New("mcp.require_auth must be true: MCP and the JSON API would run without a token")
		}
		if cfg.Tokens == nil {
			return errors.New("tokens must be configured so MCP and the JSON API can authenticate callers")
		}
	}
	for name, ds := range auth.Docsets {
		if _, ok := ds.Access.Allow["guest"]; ok {
			return fmt.Errorf("docset %q grants the guest role: anonymous access is not allowed", name)
		}
	}
	for _, ident := range auth.Identities {
		if slices.Contains(ident.Roles, "guest") {
			return fmt.Errorf("identity %q has the guest role", ident.Name)
		}
	}
	return nil
}
