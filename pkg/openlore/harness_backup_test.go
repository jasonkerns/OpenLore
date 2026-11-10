package openlore

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/harnessbackup"
)

// harnessBackupServer is the harness test server with its config pointing into a folder laid out like harness init's.
func harnessBackupServer(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	s, h := newHarnessTestServer(t)
	root := t.TempDir()
	for rel, body := range map[string]string{
		"openlore.yml":                "version: \"1\"\n",
		"config/lore.json":            "{}\n",
		"data/auth/es256.pem":         "KEY",
		"data/history/commits.jsonl":  "{}\n",
		"docs/home/sam@test.com/x.md": "private\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s.config.DataDir = filepath.Join(root, "data")
	s.config.AuthFile = filepath.Join(root, "config", "lore.json")
	return s, h, root
}

func harnessGetBackup(t *testing.T, h http.Handler, token, agent string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/harness/backup", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if agent != "" {
		req.Header.Set("X-Harness-Agent", agent)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHarnessBackupDownload(t *testing.T) {
	s, h, _ := harnessBackupServer(t)
	owner, _, _ := s.issuer.Mint(Attribution{Principal: "jason"}, ScopeFull, time.Hour)

	// Off unless the server is started with it on, and /harness/me says so.
	t.Setenv(harnessBackupEnv, "")
	if rec := harnessGetBackup(t, h, owner, ""); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), harnessBackupEnv) {
		t.Fatalf("off: %d %s", rec.Code, rec.Body.String())
	}
	if _, me := harnessDo(t, h, "GET", "/harness/me", owner, ""); me["backup"] != false {
		t.Fatalf("me = %v, want backup false", me)
	}

	t.Setenv(harnessBackupEnv, "1")
	if _, me := harnessDo(t, h, "GET", "/harness/me", owner, ""); me["backup"] != true {
		t.Fatalf("me = %v, want backup true", me)
	}
	if rec := harnessGetBackup(t, h, "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
	if rec := harnessGetBackup(t, h, owner, "Rick"); rec.Code != http.StatusForbidden {
		t.Fatalf("the owner's agent: %d, want 403", rec.Code)
	}
	code, out := harnessDo(t, h, "POST", "/harness/people", owner, `{"email":"sam@test.com"}`)
	if code != http.StatusCreated {
		t.Fatalf("add member: %d %v", code, out)
	}
	if rec := harnessGetBackup(t, h, out["token"].(string), ""); rec.Code != http.StatusForbidden {
		t.Fatalf("a member: %d, want 403", rec.Code)
	}

	rec := harnessGetBackup(t, h, owner, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), `attachment; filename="openlore-backup-`) {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := os.WriteFile(archive, rec.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	m, err := harnessbackup.Read(archive, dst)
	if err != nil {
		t.Fatalf("the download isn't a whole backup: %v", err)
	}
	if m.Files != 5 {
		t.Fatalf("files = %d, want 5", m.Files)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "docs", "home", "sam@test.com", "x.md")); string(b) != "private\n" {
		t.Fatalf("private folder = %q", b)
	}
}

func TestHarnessBackupNeedsInitLayout(t *testing.T) {
	s, h := newHarnessTestServer(t) // its lore.json sits next to data, not in config/
	owner, _, _ := s.issuer.Mint(Attribution{Principal: "jason"}, ScopeFull, time.Hour)
	t.Setenv(harnessBackupEnv, "1")
	if rec := harnessGetBackup(t, h, owner, ""); rec.Code != http.StatusConflict {
		t.Fatalf("%d %s, want 409", rec.Code, rec.Body.String())
	}
}
