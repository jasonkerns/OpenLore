package openlore

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
)

// A writable server with the harness editing routes and an owner who can write /docs.
func newHarnessMoveServer(t *testing.T) (*Server, http.Handler, string, string) {
	t.Helper()
	root := t.TempDir()
	// Not t.TempDir: on Windows the history journal is still open at cleanup, which would fail the test.
	data, err := os.MkdirTemp("", "harness-move")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(data) })
	keyless := false
	auth := &config.AuthConfig{
		AllowKeyless:    &keyless,
		UnknownIdentity: "deny",
		Roles:           map[string]config.RoleSpec{"owner": {}},
		Docsets: map[string]config.DocsetSpec{
			"docs": {Paths: []config.PathMapping{{Source: "/docs", Display: "/docs"}}, Access: config.DocsetAccess{Allow: map[string]string{"owner": "rw"}}},
		},
		Identities: []config.AuthIdentity{{Name: "jason", Roles: []string{"owner"}}, {Name: "reader", Docsets: map[string]string{"docs": "r"}}},
	}
	authFile := filepath.Join(data, "lore.json")
	b, _ := json.Marshal(auth)
	if err := os.WriteFile(authFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(root, WithReadonly(false), config.WithDataDir(data))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	s.authEnforced = true
	s.auth = auth
	s.config.AuthFile = authFile
	s.config.UnknownIdentity = "deny"
	s.config.ExternalURL = "https://lore.test"
	s.config.Tokens = &config.AuthTokensConfig{Issuer: "https://lore.test", Audience: "https://lore.test"}
	s.authorizationStore = fileAuthorizationStore{auth: s.auth}
	if err := s.initAuth(); err != nil {
		t.Fatalf("initAuth: %v", err)
	}
	for name, body := range map[string]string{"docs/a.md": "a\n", "docs/guides/one.md": "one\n", "docs/guides/deep/two.md": "two\n", "docs/archive/keep.md": "keep\n"} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	s.harnessChangeRoutes(mux)
	token, _, err := s.issuer.Mint(Attribution{Principal: "jason"}, ScopeFull, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s, mux, token, root
}

func exists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
	return err == nil
}

func TestHarnessMoveFileAndFolder(t *testing.T) {
	_, h, token, root := newHarnessMoveServer(t)

	code, out := harnessDo(t, h, "POST", "/harness/move", token, `{"from":"/docs/a.md","to":"/docs/archive/a.md"}`)
	if code != http.StatusOK || out["files"] != float64(1) {
		t.Fatalf("file move: %d %v", code, out)
	}
	if exists(root, "docs/a.md") || !exists(root, "docs/archive/a.md") {
		t.Fatal("the file didn't move")
	}

	code, out = harnessDo(t, h, "POST", "/harness/move", token, `{"from":"/docs/guides","to":"/docs/archive/guides"}`)
	if code != http.StatusOK || out["files"] != float64(2) {
		t.Fatalf("folder move: %d %v", code, out)
	}
	if exists(root, "docs/guides") || !exists(root, "docs/archive/guides/one.md") || !exists(root, "docs/archive/guides/deep/two.md") {
		t.Fatal("the folder didn't move with everything in it")
	}
}

func TestHarnessMoveRefusals(t *testing.T) {
	_, h, token, root := newHarnessMoveServer(t)
	cases := []struct {
		body string
		want int
	}{
		{`{"from":"/docs/a.md","to":"/docs/archive/keep.md"}`, http.StatusPreconditionFailed}, // never overwrites
		{`{"from":"/docs/guides","to":"/docs/guides/deep/guides"}`, http.StatusBadRequest},    // into itself
		{`{"from":"/docs/missing.md","to":"/docs/b.md"}`, http.StatusNotFound},
		{`{"from":"/docs/a.md"}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		if code, out := harnessDo(t, h, "POST", "/harness/move", token, c.body); code != c.want {
			t.Errorf("%s: %d %v, want %d", c.body, code, out, c.want)
		}
	}
	if !exists(root, "docs/a.md") || !exists(root, "docs/guides/deep/two.md") {
		t.Fatal("a refused move changed something")
	}
}

func TestHarnessDeleteFileAndFolder(t *testing.T) {
	_, h, token, root := newHarnessMoveServer(t)
	if code, out := harnessDo(t, h, "POST", "/harness/delete", token, `{"path":"/docs/a.md"}`); code != http.StatusOK || out["files"] != float64(1) {
		t.Fatalf("file delete: %d %v", code, out)
	}
	if code, out := harnessDo(t, h, "POST", "/harness/delete", token, `{"path":"/docs/guides"}`); code != http.StatusOK || out["files"] != float64(2) {
		t.Fatalf("folder delete: %d %v", code, out)
	}
	if exists(root, "docs/a.md") || exists(root, "docs/guides") || !exists(root, "docs/archive/keep.md") {
		t.Fatal("deleted the wrong things")
	}
	if code, _ := harnessDo(t, h, "POST", "/harness/delete", token, `{"path":"/docs/gone.md"}`); code != http.StatusNotFound {
		t.Fatalf("missing: %d, want 404", code)
	}
}

func TestHarnessWritable(t *testing.T) {
	s, _, token, _ := newHarnessMoveServer(t)
	claims, err := s.issuer.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.identityStore.Resolve(t.Context(), claims)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{"/docs/a.md": true, "/docs/guides": true, "/docs": false, "/": false} {
		if got := s.harnessWritable(id, p); got != want {
			t.Errorf("writable %s = %v, want %v", p, got, want)
		}
	}
	reader, _, _ := s.issuer.Mint(Attribution{Principal: "reader"}, ScopeFull, time.Hour)
	if claims, err = s.issuer.Verify(reader); err != nil {
		t.Fatal(err)
	}
	if id, err = s.identityStore.Resolve(t.Context(), claims); err != nil {
		t.Fatal(err)
	}
	if s.harnessWritable(id, "/docs/a.md") {
		t.Error("a reader can change /docs/a.md")
	}
}
