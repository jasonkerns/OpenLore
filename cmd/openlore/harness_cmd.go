package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/openlore"
)

// runHarness implements `openlore harness ...`, the server-side setup for
// Agent Harness:
//
//	openlore harness init --dir <dir> --url <public URL> [--owner <name>] [--http-port <port>]
//	openlore harness setup-code [--config <dir>/openlore.yml] [--ttl 1h]
//
// init writes a locked-down openlore.yml and lore.json (only if they don't
// exist yet) and an empty docs folder. The first time, it also prints a setup
// code. Pasting a setup code into Agent Harness makes that computer the first
// admin machine; from then on machines are added and removed from the app.
// setup-code prints a fresh one (for example after losing the only admin
// machine). A setup code is the owner's own token, so it expires quickly.
func runHarness(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "Usage:\n  openlore harness init --dir <dir> --url <public URL> [--owner <name>] [--http-port <port>]\n  openlore harness setup-code [--config <file>] [--ttl 1h]")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "init":
		return harnessInit(args[1:], stdout, stderr)
	case "setup-code":
		f := flag.NewFlagSet("harness setup-code", flag.ContinueOnError)
		f.SetOutput(stderr)
		configPath := f.String("config", "./openlore.yml", "path to openlore.yml")
		ttl := f.Duration("ttl", time.Hour, "how long the code works")
		if err := f.Parse(args[1:]); err != nil {
			return 2
		}
		return printSetupCode(*configPath, *ttl, stdout, stderr)
	default:
		return usage()
	}
}

func harnessInit(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("harness init", flag.ContinueOnError)
	f.SetOutput(stderr)
	dir := f.String("dir", ".", "folder for the config, data and docs")
	publicURL := f.String("url", "", "the address clients use, e.g. https://lore.example.com (required)")
	owner := f.String("owner", "owner", "name of the owner identity")
	httpPort := f.Int("http-port", 8080, "port the server listens on (plain HTTP; HTTPS is the load balancer's job)")
	if err := f.Parse(args); err != nil {
		return 2
	}
	url := strings.TrimRight(*publicURL, "/")
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://localhost") && !strings.HasPrefix(url, "http://127.0.0.1") {
		fmt.Fprintln(stderr, "error: --url must be https:// (or http://localhost for development)")
		return 2
	}
	if !harnessName(*owner) {
		fmt.Fprintln(stderr, "error: --owner must be lowercase letters, digits and dashes")
		return 2
	}
	root, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	for _, d := range []string{"config", "data", "docs"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}
	ymlPath := filepath.Join(root, "openlore.yml")
	authPath := filepath.Join(root, "config", "lore.json")
	slash := filepath.ToSlash
	yml := strings.Join([]string{
		"# Written by `openlore harness init`. Edit freely; init won't overwrite it.",
		`version: "1"`,
		"port: 0 # SSH off",
		fmt.Sprintf("http_port: %d", *httpPort),
		"external_url: " + url,
		"metrics_port: 0",
		"default_cwd: /",
		"auth_file: " + slash(authPath),
		"data_dir: " + slash(filepath.Join(root, "data")),
		"readonly: false",
		"mcp:",
		"  enabled: true",
		"  path: /mcp",
		"  require_auth: true",
		"api:",
		"  enabled: true",
		"  path: /api",
		"tokens:",
		"  issuer: " + url,
		"  audience: " + url,
		"  access_ttl: 1h",
		"  refresh_ttl: 720h",
		"",
	}, "\n")
	if wrote, err := writeIfMissing(ymlPath, []byte(yml)); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	} else if wrote {
		fmt.Fprintf(stdout, "Wrote %s\n", ymlPath)
	}
	auth := map[string]any{
		"allow_keyless":    false,
		"unknown_identity": "deny",
		"default_cwd":      "/",
		"roles":            map[string]any{"owner": map[string]any{"allow": map[string]any{"capabilities": []string{"lore:config:edit", "lore:access:view"}}}},
		"docsets":          map[string]any{"docs": map[string]any{"paths": []string{"/"}, "access": map[string]any{"allow": map[string]string{"owner": "rw"}}}},
		"identities":       []any{map[string]any{"name": *owner, "roles": []string{"owner"}}},
	}
	b, _ := json.MarshalIndent(auth, "", "  ")
	wroteAuth, err := writeIfMissing(authPath, append(b, '\n'))
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if !wroteAuth {
		fmt.Fprintln(stdout, "Already set up. For a new setup code: openlore harness setup-code --config "+ymlPath)
		return 0
	}
	fmt.Fprintf(stdout, "Wrote %s (owner: %s)\n", authPath, *owner)
	return printSetupCode(ymlPath, time.Hour, stdout, stderr)
}

func printSetupCode(configPath string, ttl time.Duration, stdout, stderr io.Writer) int {
	cfg, err := config.New(config.WithConfigFile(configPath))
	if err != nil {
		fmt.Fprintf(stderr, "error: loading %s: %v\n", configPath, err)
		return 1
	}
	if err := requireNoAnonymous(cfg); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	owner, err := harnessOwner(cfg.AuthFile)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	issuer, err := openlore.NewIssuerFromConfig(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	token, exp, err := issuer.Mint(openlore.Attribution{Principal: owner}, openlore.ScopeFull, ttl)
	if err != nil {
		fmt.Fprintf(stderr, "error: minting the setup token: %v\n", err)
		return 1
	}
	payload, _ := json.Marshal(map[string]string{"u": cfg.HTTPBaseURL(), "t": token})
	fmt.Fprintf(stdout, "\nSetup code (works until %s; paste it into Agent Harness → Settings → Knowledge base):\n\n", exp.Local().Format(time.Kitchen))
	fmt.Fprintf(stdout, "olsetup1.%s\n\n", base64.RawURLEncoding.EncodeToString(payload))
	return 0
}

// harnessOwner is the identity that holds the owner role (the first one, if
// someone added more).
func harnessOwner(authFile string) (string, error) {
	auth, err := config.LoadAuthConfig(authFile)
	if err != nil {
		return "", err
	}
	for _, id := range auth.Identities {
		for _, r := range id.Roles {
			if r == "owner" {
				return id.Name, nil
			}
		}
	}
	return "", errors.New("no identity in lore.json has the owner role")
}

func harnessName(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func writeIfMissing(path string, data []byte) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	_, err = f.Write(data)
	return true, err
}
