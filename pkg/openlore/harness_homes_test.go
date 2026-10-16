package openlore

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// Two people, a@test.com (member) and b@test.com (admin), each with a private
// folder in /home, plus the shared docs.
func newHomesTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	keyless := false
	auth := &config.AuthConfig{
		AllowKeyless:    &keyless,
		UnknownIdentity: "deny",
		Roles:           map[string]config.RoleSpec{"owner": {Allow: config.CapabilityRules{Capabilities: []string{"lore:config:edit"}}}, "member": {}},
		Docsets: map[string]config.DocsetSpec{
			"docs":  {Paths: []config.PathMapping{{Source: "/docs", Display: "/docs"}}, Access: config.DocsetAccess{Allow: map[string]string{"owner": "rw", "member": "rw"}}},
			"homes": {Paths: []config.PathMapping{{Source: "/home", Display: "/home"}}, Access: config.DocsetAccess{Allow: map[string]string{"owner": "rw", "member": "rw"}}},
		},
		Identities: []config.AuthIdentity{{Name: "a@test.com", Roles: []string{"member"}}, {Name: "b@test.com", Roles: []string{"owner"}}},
	}
	authFile := filepath.Join(dir, "lore.json")
	b, _ := json.Marshal(auth)
	if err := os.WriteFile(authFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
	merge := NewMergeFS()
	merge.Mount("docs", NewFSAdapter(fstest.MapFS{"shared.md": {Data: []byte("shared\n")}}))
	merge.Mount("home", NewFSAdapter(fstest.MapFS{
		"a@test.com/notes.md":  {Data: []byte("A's notes\n")},
		"b@test.com/secret.md": {Data: []byte("B_PRIVATE\n")},
	}))
	s := &Server{
		merge:        merge,
		authEnforced: true,
		grants:       newGrantRegistry(),
		auth:         auth,
		config:       config.Config{DataDir: dir, AuthFile: authFile, UnknownIdentity: "deny", ExternalURL: "https://lore.test", Tokens: &config.AuthTokensConfig{Issuer: "https://lore.test", Audience: "https://lore.test"}},
	}
	s.authorizationStore = fileAuthorizationStore{auth: s.auth}
	if err := s.initAuth(); err != nil {
		t.Fatalf("initAuth: %v", err)
	}
	return s
}

func person(name string) Identity {
	return Identity{IdentityName: name, Principal: AuthenticatedPrincipal{IdentityName: name}, Attribution: Attribution{Principal: name}, Scopes: []string{ScopeFull}}
}

func names(t *testing.T, fsys vfs.FileSystem, dir string) []string {
	t.Helper()
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestHomesAreVisibleOnlyToTheirOwner(t *testing.T) {
	s := newHomesTestServer(t)
	agent := person("a@test.com")
	agent.Attribution.Agent, agent.Attribution.AgentID = "Rick", "a1"
	for _, who := range []struct {
		label string
		id    Identity
		own   string
		other string
	}{
		{"a member", person("a@test.com"), "/home/a@test.com/notes.md", "/home/b@test.com/secret.md"},
		{"an admin", person("b@test.com"), "/home/b@test.com/secret.md", "/home/a@test.com/notes.md"},
		{"a member's agent", agent, "/home/a@test.com/notes.md", "/home/b@test.com/secret.md"},
	} {
		t.Run(who.label, func(t *testing.T) {
			fsys := s.buildCanonicalSessionFS(who.id)
			listed := names(t, fsys, "/home")
			if len(listed) != 1 || "/home/"+listed[0] != filepath.ToSlash(filepath.Dir(who.own)) {
				t.Fatalf("/home lists %v, want only their own folder", listed)
			}
			if got, err := fsys.ReadFile(who.own); err != nil || len(got) == 0 {
				t.Fatalf("own file: %q %v", got, err)
			}
			if got, err := fsys.ReadFile(who.other); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("someone else's file: %q %v, want not found", got, err)
			}
			if _, err := fsys.Stat(filepath.ToSlash(filepath.Dir(who.other))); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("someone else's folder: %v, want not found", err)
			}
			if _, err := fsys.ReadFile("/docs/shared.md"); err != nil {
				t.Fatalf("shared docs: %v", err)
			}
		})
	}
}

func TestHomesAreWritableOnlyByTheirOwner(t *testing.T) {
	s := newHomesTestServer(t)
	a, b := person("a@test.com"), person("b@test.com")
	for _, c := range []struct {
		id     Identity
		action vfs.ChangeAction
		path   string
		want   bool
	}{
		{a, vfs.ChangeActionWrite, "/home/a@test.com/new.md", true},
		{a, vfs.ChangeActionMkdirAll, "/home/a@test.com/memory/projects/golf", true},
		{a, vfs.ChangeActionRemove, "/home/a@test.com/notes.md", true},
		{a, vfs.ChangeActionWrite, "/home/b@test.com/new.md", false},
		{a, vfs.ChangeActionRemove, "/home/b@test.com/secret.md", false},
		{a, vfs.ChangeActionMkdirAll, "/home/c@test.com", false},
		{a, vfs.ChangeActionWrite, "/home/loose.md", false},
		{b, vfs.ChangeActionWrite, "/home/a@test.com/new.md", false}, // admins too
		{b, vfs.ChangeActionWrite, "/home/b@test.com/new.md", true},
		{a, vfs.ChangeActionWrite, "/docs/new.md", true},
	} {
		if got := s.identityCanWrite(c.id, c.action, c.path); got != c.want {
			t.Errorf("%s %s %s = %v, want %v", c.id.IdentityName, c.action, c.path, got, c.want)
		}
	}
}

func TestHomesStayOutOfOtherPeoplesHistory(t *testing.T) {
	s := newHomesTestServer(t)
	scope := s.historyFor(person("a@test.com"), HistoryQuery{})
	for path, want := range map[string]bool{
		"/home/a@test.com/notes.md":  true,
		"/home/b@test.com/secret.md": false,
		"/docs/shared.md":            true,
	} {
		if got := historyReadable(scope.Roots, scope.Boundaries, path); got != want {
			t.Errorf("history for a@test.com shows %s = %v, want %v", path, got, want)
		}
	}
}

func TestHomeIsTheShellsHome(t *testing.T) {
	s := newHomesTestServer(t)
	if got := s.resolveSessionIdentity(person("a@test.com")).HomeDir; got != "/home/a@test.com" {
		t.Fatalf("HomeDir = %q", got)
	}
	delete(s.auth.Docsets, harnessHomesDocset)
	if got := s.homeFolder(person("a@test.com")); got != "" {
		t.Fatalf("without the homes docset: %q", got)
	}
}

// Analytics (most-read files, largest documents, searches) decides with
// grantsForPath, so nothing in someone else's folder shows up there either.
func TestHomesGrantNothingInOtherPeoplesFolders(t *testing.T) {
	s := newHomesTestServer(t)
	a := person("a@test.com")
	if _, _, ok := s.grantsForPath(a, "/home/a@test.com/notes.md"); !ok {
		t.Fatal("no grant in their own folder")
	}
	for _, p := range []string{"/home/b@test.com/secret.md", "/home/b@test.com", "/home"} {
		if _, _, ok := s.grantsForPath(a, p); ok {
			t.Errorf("a@test.com has a grant on %s", p)
		}
	}
	events := &dashboardEventSource{server: s, identity: s.resolveSessionIdentity(a)}
	if events.pathReadable("/home/b@test.com/secret.md", "/") {
		t.Fatal("analytics would show someone else's file")
	}
}
