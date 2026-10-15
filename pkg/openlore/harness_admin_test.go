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
		Roles:           map[string]config.RoleSpec{"owner": {Allow: config.CapabilityRules{Capabilities: []string{"lore:config:edit"}}}, "member": {}},
		Docsets: map[string]config.DocsetSpec{
			"docs": {Paths: []config.PathMapping{{Source: "/docs", Display: "/docs"}}, Access: config.DocsetAccess{Allow: map[string]string{"owner": "rw", "member": "rw"}}},
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

func TestHarnessAdmin_PeopleLifecycle(t *testing.T) {
	s, h := newHarnessTestServer(t)
	owner, _, err := s.issuer.Mint(Attribution{Principal: "jason"}, ScopeFull, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if code, _ := harnessDo(t, h, "GET", "/harness/me", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", code)
	}
	// The setup code's owner token makes the first admin, named by email.
	code, out := harnessDo(t, h, "POST", "/harness/people", owner, `{"email":"JK@Test.com","admin":true}`)
	if code != http.StatusCreated {
		t.Fatalf("add admin: %d %v", code, out)
	}
	jk := out["token"].(string)
	if p := out["person"].(map[string]any); p["email"] != "jk@test.com" || p["admin"] != true {
		t.Fatalf("person = %v", p)
	}
	_, me := harnessDo(t, h, "GET", "/harness/me", jk, "")
	if me["principal"] != "jk@test.com" || me["admin"] != true {
		t.Fatalf("admin me = %v", me)
	}
	if code, _ := harnessDo(t, h, "POST", "/harness/people", jk, `{"email":"jk@test.com"}`); code != http.StatusConflict {
		t.Fatalf("duplicate: %d, want 409", code)
	}
	for _, bad := range []string{`{"email":"not an email"}`, `{"email":"a/b@c.d"}`, `{"email":"x@y"}`} {
		if code, _ := harnessDo(t, h, "POST", "/harness/people", jk, bad); code != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400", bad, code)
		}
	}

	// A member: reads and writes documents, manages nothing.
	code, out = harnessDo(t, h, "POST", "/harness/people", jk, `{"email":"sam@test.com"}`)
	if code != http.StatusCreated {
		t.Fatalf("add member: %d %v", code, out)
	}
	sam := out["token"].(string)
	_, me = harnessDo(t, h, "GET", "/harness/me", sam, "")
	if me["principal"] != "sam@test.com" || me["admin"] != false {
		t.Fatalf("member me = %v", me)
	}
	for _, req := range [][3]string{{"GET", "/harness/people", ""}, {"POST", "/harness/people", `{"email":"evil@test.com","admin":true}`}, {"DELETE", "/harness/people/jk@test.com", ""}, {"POST", "/harness/people/sam@test.com/code", ""}, {"POST", "/harness/passkey", ""}} {
		if code, _ := harnessDo(t, h, req[0], req[1], sam, req[2]); code != http.StatusForbidden {
			t.Fatalf("member %s %s: %d, want 403", req[0], req[1], code)
		}
	}

	_, list := harnessDo(t, h, "GET", "/harness/people", jk, "")
	b, _ := json.Marshal(list["people"])
	if string(b) != `[{"admin":true,"email":"jk@test.com"},{"admin":false,"email":"sam@test.com"}]` {
		t.Fatalf("people = %s", b)
	}

	// Another code for an existing person (a second computer); both work.
	code, out = harnessDo(t, h, "POST", "/harness/people/sam@test.com/code", jk, "")
	if code != http.StatusOK {
		t.Fatalf("new code: %d %v", code, out)
	}
	sam2 := out["token"].(string)
	if code, _ := harnessDo(t, h, "POST", "/harness/people/nobody@test.com/code", jk, ""); code != http.StatusNotFound {
		t.Fatalf("code for nobody: %d, want 404", code)
	}

	if code, _ := harnessDo(t, h, "DELETE", "/harness/people/jk@test.com", jk, ""); code != http.StatusConflict {
		t.Fatalf("self-removal: %d, want 409", code)
	}
	if code, _ := harnessDo(t, h, "DELETE", "/harness/people/sam@test.com", jk, ""); code != http.StatusNoContent {
		t.Fatalf("remove: %d, want 204", code)
	}
	for _, tok := range []string{sam, sam2} {
		if code, _ := harnessDo(t, h, "GET", "/harness/me", tok, ""); code != http.StatusForbidden {
			t.Fatalf("removed person's token: %d, want 403", code)
		}
	}
	if code, _ := harnessDo(t, h, "DELETE", "/harness/people/sam@test.com", jk, ""); code != http.StatusNotFound {
		t.Fatalf("remove twice: %d, want 404", code)
	}
	// Only email identities are people: the bootstrap owner can't be removed this way.
	if code, _ := harnessDo(t, h, "DELETE", "/harness/people/jason", jk, ""); code != http.StatusNotFound {
		t.Fatalf("remove non-person: %d, want 404", code)
	}

	saved, err := config.LoadAuthConfig(s.config.AuthFile)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, id := range saved.Identities {
		names = append(names, id.Name)
	}
	if strings.Join(names, ",") != "jason,jk@test.com" {
		t.Fatalf("identities on disk = %v", names)
	}
}

func TestHarnessAgentLabel(t *testing.T) {
	req := httptest.NewRequest("GET", "/mcp", nil)
	req.Header.Set("X-Harness-Agent", "Rick%20%E2%9C%93%0Aevil")
	req.Header.Set("X-Harness-Agent-Id", strings.Repeat("x", 200))
	name, id := harnessAgentLabel(req)
	if name != "Rick ✓evil" || len(id) != 80 {
		t.Fatalf("label = %q, %d", name, len(id))
	}
	if name, id := harnessAgentLabel(httptest.NewRequest("GET", "/mcp", nil)); name != "" || id != "" {
		t.Fatalf("no headers = %q %q", name, id)
	}
	a := Attribution{Principal: "jk@test.com", Agent: "Rick", AgentID: "a1"}
	if a.String() != "jk@test.com" || a.Label() != "jk@test.com:Rick" {
		t.Fatalf("String=%q Label=%q", a.String(), a.Label())
	}
	if (Attribution{Principal: "o", Actor: "m"}).Label() != "o/m" {
		t.Fatal("label without agent")
	}
}

func TestHarnessHistoryByAgent(t *testing.T) {
	store := NewJSONLHistoryStore(t.TempDir())
	now := time.Now()
	records := []HistoryRecord{
		{Time: now, Attribution: Attribution{Principal: "jk@test.com", Agent: "Rick", AgentID: "a1"}, FileKey: "/docs/a.md", Action: "write"},
		{Time: now, Attribution: Attribution{Principal: "jk@test.com", Agent: "Morty", AgentID: "a2"}, FileKey: "/docs/b.md", Action: "write"},
		{Time: now, Attribution: Attribution{Principal: "sam@test.com", Agent: "Rick", AgentID: "a1"}, FileKey: "/docs/c.md", Action: "write"},
		{Time: now, Attribution: Attribution{Principal: "jk@test.com"}, FileKey: "/docs/d.md", Action: "write"},
	}
	if err := store.Record(t.Context(), records); err != nil {
		t.Fatal(err)
	}
	page, err := store.Query(t.Context(), HistoryQuery{Roots: []string{"/docs"}, Principal: "jk@test.com", AgentID: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].FileKey != "/docs/a.md" || page.Records[0].Attribution.Label() != "jk@test.com:Rick" {
		t.Fatalf("records = %+v", page.Records)
	}
}

func TestHarnessActivityNeedsAnAgent(t *testing.T) {
	s, h := newHarnessTestServer(t)
	owner, _, _ := s.issuer.Mint(Attribution{Principal: "jason"}, ScopeFull, time.Hour)
	if code, _ := harnessDo(t, h, "GET", "/harness/activity", owner, ""); code != http.StatusBadRequest {
		t.Fatalf("no agent: %d, want 400", code)
	}
	code, out := harnessDo(t, h, "GET", "/harness/activity?agent_id=a1", owner, "")
	if code != http.StatusOK || out["available"] != false {
		t.Fatalf("no history: %d %v", code, out)
	}
}

func TestHarnessAdmin_PasskeysOff(t *testing.T) {
	s, h := newHarnessTestServer(t)
	owner, _, _ := s.issuer.Mint(Attribution{Principal: "jason"}, ScopeFull, time.Hour)
	if code, _ := harnessDo(t, h, "POST", "/harness/passkey", owner, ""); code != http.StatusConflict {
		t.Fatalf("passkey link with passkeys off: %d, want 409", code)
	}
}
