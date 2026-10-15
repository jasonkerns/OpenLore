package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"regexp"
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

	// A second run keeps everything and prints no new code.
	out.Reset()
	if code := runHarness([]string{"init", "--dir", dir, "--url", "https://other.test"}, &out, &errOut); code != 0 {
		t.Fatalf("second init exit %d", code)
	}
	if strings.Contains(out.String(), "olsetup1.") || !strings.Contains(out.String(), "Already set up") {
		t.Fatalf("second init output:\n%s", out.String())
	}

	if code := runHarness([]string{"init", "--dir", t.TempDir(), "--url", "http://lore.test"}, &out, &errOut); code == 0 {
		t.Fatal("init accepted a plain-http public URL")
	}
}
