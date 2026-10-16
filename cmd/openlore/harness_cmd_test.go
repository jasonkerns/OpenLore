package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/openlore"
)

func TestHarnessInit(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := runHarness([]string{"init", "--dir", dir, "--url", "https://lore.test/", "--owner", "jason"}, &out, &errOut); code != 0 {
		t.Fatalf("init exit %d: %s", code, errOut.String())
	}
	m := regexp.MustCompile(`olsetup1\.([A-Za-z0-9_-]+)`).FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no setup code in output:\n%s", out.String())
	}
	raw, err := base64.RawURLEncoding.DecodeString(m[1])
	if err != nil {
		t.Fatal(err)
	}
	var code struct{ U, T string }
	if err := json.Unmarshal(raw, &code); err != nil {
		t.Fatal(err)
	}
	if code.U != "https://lore.test" {
		t.Fatalf("setup code url = %q", code.U)
	}

	// The config it writes passes the no-anonymous-access check, and the
	// setup code is a token for the owner.
	cfg, err := config.New(config.WithConfigFile(filepath.Join(dir, "openlore.yml")))
	if err != nil {
		t.Fatal(err)
	}
	if err := requireNoAnonymous(cfg); err != nil {
		t.Fatalf("init config is not locked down: %v", err)
	}
	if cfg.Port != 0 {
		t.Fatalf("SSH port = %d, want 0 (off)", cfg.Port)
	}
	issuer, err := openlore.NewIssuerFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := issuer.Verify(code.T)
	if err != nil || claims.Subject != "jason" || claims.Actor != "" {
		t.Fatalf("setup token: claims=%+v err=%v", claims, err)
	}

	if !slices.Contains(cfg.Files.Allowed, "*.sh") || !slices.Contains(cfg.Files.Allowed, "*.md") || !slices.Contains(cfg.Files.Allowed, "*.png") {
		t.Fatalf("allowed files = %v, want the defaults plus scripts", cfg.Files.Allowed)
	}
	if !cfg.Plugins.Skills.Enabled {
		t.Fatal("init config doesn't turn on Agent Skills")
	}

	// The empty docs folder gets the starter layout; skills/ stays empty, since
	// each child of a skills collection must be a skill.
	for rel := range harnessLayout {
		if _, err := os.Stat(filepath.Join(dir, "docs", filepath.FromSlash(rel))); err != nil {
			t.Fatalf("starter layout: %v", err)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "docs", "skills")); len(entries) != 0 {
		t.Fatalf("skills/ has %d entries, want none", len(entries))
	}

	// A second run keeps everything and prints no new code, and doesn't seed a
	// docs folder that has something in it.
	if err := os.Remove(filepath.Join(dir, "docs", "ideas", "README.md")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runHarness([]string{"init", "--dir", dir, "--url", "https://other.test"}, &out, &errOut); code != 0 {
		t.Fatalf("second init exit %d", code)
	}
	if strings.Contains(out.String(), "olsetup1.") || !strings.Contains(out.String(), "Already set up") {
		t.Fatalf("second init output:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "docs", "ideas", "README.md")); err == nil {
		t.Fatal("second init seeded a docs folder that wasn't empty")
	}

	if code := runHarness([]string{"init", "--dir", t.TempDir(), "--url", "http://lore.test"}, &out, &errOut); code == 0 {
		t.Fatal("init accepted a plain-http public URL")
	}
}

func TestHarnessInitAddsPrivateFolders(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := runHarness([]string{"init", "--dir", dir, "--url", "https://lore.test"}, &out, &errOut); code != 0 {
		t.Fatalf("init: %s", errOut.String())
	}
	authPath := filepath.Join(dir, "config", "lore.json")
	cfg, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Docsets["homes"]; !ok {
		t.Fatal("a new server has no homes docset")
	}

	// A server set up before private folders: no homes docset, and someone already there.
	var auth map[string]any
	raw, _ := os.ReadFile(authPath)
	_ = json.Unmarshal(raw, &auth)
	delete(auth["docsets"].(map[string]any), "homes")
	auth["identities"] = append(auth["identities"].([]any), map[string]any{"name": "jk@test.com", "roles": []string{"member"}})
	b, _ := json.Marshal(auth)
	if err := os.WriteFile(authPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runHarness([]string{"init", "--dir", dir, "--url", "https://lore.test"}, &out, &errOut); code != 0 {
		t.Fatalf("second init: %s", errOut.String())
	}
	if !strings.Contains(out.String(), "Added private folders") {
		t.Fatalf("output: %s", out.String())
	}
	if cfg, err = config.LoadAuthConfig(authPath); err != nil || cfg.Docsets["homes"].Paths[0].Display != "/home" {
		t.Fatalf("homes docset not added: %v %+v", err, cfg.Docsets["homes"])
	}
	if info, err := os.Stat(filepath.Join(dir, "docs", "home", "jk@test.com")); err != nil || !info.IsDir() {
		t.Fatalf("no folder for jk@test.com: %v", err)
	}
}
