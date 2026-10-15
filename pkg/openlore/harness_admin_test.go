package openlore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
)

func newHarnessTestServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	keyless := false
	auth := &config.AuthConfig{
		AllowKeyless:    &keyless,
		UnknownIdentity: "deny",
		Roles:           map[string]config.RoleSpec{"owner": {Allow: config.CapabilityRules{Capabilities: []string{"lore:config:edit"}}}},
		Docsets: map[string]config.DocsetSpec{
			"docs": {Paths: []config.PathMapping{{Source: "/docs", Display: "/docs"}}, Access: config.DocsetAccess{Allow: map[string]string{"owner": "rw"}}},
		},
		Identities: []config.AuthIdentity{{Name: "jason", Roles: []string{"owner"}}},
	}
	authFile := filepath.Join(dir, "lore.json")
	b, _ := json.Marshal(auth)
	if err := os.WriteFile(authFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
	merge := NewMergeFS()
	merge.Mount("docs", NewFSAdapter(fstest.MapFS{"a.md": {Data: []byte("a\n")}}))
	s := &Server{
		merge:        merge,
		authEnforced: true,
		grants:       newGrantRegistry(),
		auth:         auth,
		config: config.Config{
			DataDir:         dir,
			AuthFile:        authFile,
			UnknownIdentity: "deny",
			ExternalURL:     "https://lore.test",
			Tokens:          &config.AuthTokensConfig{Issuer: "https://lore.test", Audience: "https://lore.test"},
		},
	}
	s.authorizationStore = fileAuthorizationStore{auth: s.auth}
	if err := s.initAuth(); err != nil {
		t.Fatalf("initAuth: %v", err)
	}
	mux := http.NewServeMux()
	s.harnessRoutes()(mux)
	return s, mux
}

func harnessDo(t *testing.T, h http.Handler, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestHarnessAdmin_MachineLifecycle(t *testing.T) {
	s, h := newHarnessTestServer(t)
	owner, _, err := s.issuer.Mint(Attribution{Principal: "jason"}, ScopeFull, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if code, _ := harnessDo(t, h, "GET", "/harness/me", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", code)
	}
	code, out := harnessDo(t, h, "POST", "/harness/machines", owner, `{"name":"pc","admin":true}`)
	if code != http.StatusCreated {
		t.Fatalf("add admin machine: %d %v", code, out)
	}
	pc := out["token"].(string)
	if code, _ := harnessDo(t, h, "POST", "/harness/machines", pc, `{"name":"pc"}`); code != http.StatusConflict {
		t.Fatalf("duplicate: %d, want 409", code)
	}
	if code, _ := harnessDo(t, h, "POST", "/harness/machines", pc, `{"name":"Not Valid"}`); code != http.StatusBadRequest {
		t.Fatalf("bad name: %d, want 400", code)
	}
	code, out = harnessDo(t, h, "POST", "/harness/machines", pc, `{"name":"laptop"}`)
	if code != http.StatusCreated {
		t.Fatalf("add machine from admin machine: %d %v", code, out)
	}
	laptop := out["token"].(string)

	_, me := harnessDo(t, h, "GET", "/harness/me", laptop, "")
	if me["actor"] != "machine-laptop" || me["principal"] != "jason" || me["admin"] != false {
		t.Fatalf("laptop me = %v", me)
	}
	for _, req := range [][3]string{{"GET", "/harness/machines", ""}, {"POST", "/harness/machines", `{"name":"evil","admin":true}`}, {"DELETE", "/harness/machines/pc", ""}, {"POST", "/harness/passkey", ""}} {
		if code, _ := harnessDo(t, h, req[0], req[1], laptop, req[2]); code != http.StatusForbidden {
			t.Fatalf("non-admin %s %s: %d, want 403", req[0], req[1], code)
		}
	}

	if code, _ := harnessDo(t, h, "DELETE", "/harness/machines/pc", pc, ""); code != http.StatusConflict {
		t.Fatalf("self-removal: %d, want 409", code)
	}
	if code, _ := harnessDo(t, h, "DELETE", "/harness/machines/laptop", pc, ""); code != http.StatusNoContent {
		t.Fatalf("remove: %d, want 204", code)
	}
	if code, _ := harnessDo(t, h, "GET", "/harness/me", laptop, ""); code != http.StatusForbidden {
		t.Fatalf("removed machine's token: %d, want 403", code)
	}
	if code, _ := harnessDo(t, h, "DELETE", "/harness/machines/laptop", pc, ""); code != http.StatusNotFound {
		t.Fatalf("remove twice: %d, want 404", code)
	}

	// The change is on disk, so it survives a restart.
	saved, err := config.LoadAuthConfig(s.config.AuthFile)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, id := range saved.Identities {
		names = append(names, id.Name)
	}
	if strings.Join(names, ",") != "jason,machine-pc" {
		t.Fatalf("identities on disk = %v", names)
	}
}

func TestHarnessAdmin_PasskeysOff(t *testing.T) {
	s, h := newHarnessTestServer(t)
	owner, _, _ := s.issuer.Mint(Attribution{Principal: "jason"}, ScopeFull, time.Hour)
	if code, _ := harnessDo(t, h, "POST", "/harness/passkey", owner, ""); code != http.StatusConflict {
		t.Fatalf("passkey link with passkeys off: %d, want 409", code)
	}
}
