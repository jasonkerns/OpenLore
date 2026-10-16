package openlore

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/aakarim/go-openlore/internal/config"
)

// The Agent Harness admin API. Each person using Agent Harness is an identity
// named by their email, accountable for everything done with their tokens:
// their own edits and their agents' writes, which carry the agent as a label
// (Attribution.Agent; see harnessAgentLabel). Admins (role owner, with
// lore:config:edit) manage people from the app: add them (getting a code for
// their computer), issue further codes (another computer, a reinstall), remove
// them (which revokes every token they hold), and get passkey links for the
// dashboard. Everyone can list their own agents' changes.
//
// Every route needs a bearer token; the management routes also need
// lore:config:edit, the same capability that guards lore.json in the shell.

const (
	harnessTokenTTL   = 365 * 24 * time.Hour
	harnessConfigEdit = "lore:config:edit"
	harnessAdminRole  = "owner"
	harnessMemberRole = "member"
)

// Deliberately loose: one "@", something on each side, no spaces or slashes.
var harnessEmail = regexp.MustCompile(`^[^\s@/:]+@[^\s@/:]+\.[^\s@/:]+$`)

type harnessPerson struct {
	Email string `json:"email"`
	Admin bool   `json:"admin"`
}

func (s *Server) harnessRoutes() HTTPRouteRegistrar {
	return func(mux *http.ServeMux) {
		// For load balancer health checks; says nothing about the server.
		mux.HandleFunc("GET /harness/health", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte("ok\n"))
		})
		mux.Handle("GET /harness/me", s.harnessAuth(false, s.harnessMe))
		mux.Handle("GET /harness/activity", s.harnessAuth(false, s.harnessActivity))
		mux.Handle("GET /harness/people", s.harnessAuth(true, s.harnessListPeople))
		mux.Handle("POST /harness/people", s.harnessAuth(true, s.harnessAddPerson))
		mux.Handle("POST /harness/people/{email}/code", s.harnessAuth(true, s.harnessPersonCode))
		mux.Handle("DELETE /harness/people/{email}", s.harnessAuth(true, s.harnessRemovePerson))
		mux.Handle("POST /harness/passkey", s.harnessAuth(true, s.harnessPasskeyLink))
		mux.Handle("POST /harness/folder", s.harnessAuth(false, s.harnessMakeFolder))
		s.harnessChangeRoutes(mux)
	}
}

// harnessAgentLabel reads the agent an Agent Harness request is from. The
// harness's relay sets the headers (percent-encoded, so any name survives); a
// caller could claim any agent, but only within its own identity, which stays
// the accountable party. Control characters are dropped and lengths capped.
func harnessAgentLabel(r *http.Request) (name, id string) {
	clean := func(raw string, max int) string {
		if decoded, err := url.PathUnescape(raw); err == nil {
			raw = decoded
		}
		raw = strings.Map(func(c rune) rune {
			if unicode.IsControl(c) {
				return -1
			}
			return c
		}, strings.TrimSpace(raw))
		if r := []rune(raw); len(r) > max {
			raw = string(r[:max])
		}
		return raw
	}
	return clean(r.Header.Get("X-Harness-Agent"), 80), clean(r.Header.Get("X-Harness-Agent-Id"), 80)
}

// harnessAuth resolves the bearer token; with admin set it also requires
// lore:config:edit. Cookies are never accepted, so these routes need no CSRF
// protection.
func (s *Server) harnessAuth(admin bool, next func(http.ResponseWriter, *http.Request, Identity)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		token := bearerToken(r)
		if token == "" || s.issuer == nil || s.identityStore == nil {
			harnessError(w, http.StatusUnauthorized, "a bearer token is required")
			return
		}
		claims, err := s.issuer.Verify(token)
		if err != nil {
			harnessError(w, http.StatusUnauthorized, "the token is not valid")
			return
		}
		id, err := s.identityStore.Resolve(r.Context(), claims)
		if err != nil {
			harnessError(w, http.StatusForbidden, "the token names no identity on this server")
			return
		}
		// Writes the app makes for an agent (publishing its specs) carry its label, as over MCP. Set before the admin
		// check: a request labelled as an agent's is never an admin's (see agentDeniedCapabilities).
		id.Attribution.Agent, id.Attribution.AgentID = harnessAgentLabel(r)
		if admin && !s.harnessIsAdmin(id) {
			harnessError(w, http.StatusForbidden, "only an admin can manage the knowledge base")
			return
		}
		next(w, r, id)
	})
}

func (s *Server) harnessIsAdmin(id Identity) bool {
	return scopeGrantsWrite(id.Scopes) && s.hasCurrentCapability(id, harnessConfigEdit)
}

func (s *Server) harnessMe(w http.ResponseWriter, _ *http.Request, id Identity) {
	harnessJSON(w, http.StatusOK, map[string]any{
		"principal": id.Attribution.Principal,
		"actor":     id.Attribution.Actor,
		"admin":     s.harnessIsAdmin(id),
		"passkeys":  s.passkeys != nil,
	})
}

func (s *Server) harnessListPeople(w http.ResponseWriter, _ *http.Request, _ Identity) {
	people := []harnessPerson{}
	for _, ident := range s.currentAuth().Identities {
		if harnessEmail.MatchString(ident.Name) {
			people = append(people, harnessPerson{Email: ident.Name, Admin: slices.Contains(ident.Roles, harnessAdminRole)})
		}
	}
	harnessJSON(w, http.StatusOK, map[string]any{"people": people})
}

func (s *Server) harnessAddPerson(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		Email string `json:"email"`
		Admin bool   `json:"admin"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		harnessError(w, http.StatusBadRequest, "expected JSON {\"email\": ..., \"admin\": ...}")
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if !harnessEmail.MatchString(email) || len(email) > 254 {
		harnessError(w, http.StatusBadRequest, "that doesn't look like an email address")
		return
	}
	role := harnessMemberRole
	if body.Admin {
		role = harnessAdminRole
	}
	errExists := errors.New("exists")
	err := s.updateAuth(id.attribution(), "harness.person.add", func(auth *config.AuthConfig) error {
		for _, existing := range auth.Identities {
			if strings.EqualFold(existing.Name, email) {
				return errExists
			}
		}
		if role == harnessMemberRole && !harnessHasMemberRole(auth) {
			return errNoMemberRole
		}
		auth.Identities = append(auth.Identities, config.AuthIdentity{Name: email, Comment: "Agent Harness person", Roles: []string{role}})
		return nil
	})
	if errors.Is(err, errNoMemberRole) {
		harnessError(w, http.StatusConflict, "lore.json has no member role with access to the documents (servers set up before people existed); add role \"member\" with the same docset grants as \"owner\" and restart the server")
		return
	}
	if errors.Is(err, errExists) {
		harnessError(w, http.StatusConflict, fmt.Sprintf("%s is already on this server; give them a new code instead", email))
		return
	}
	if err != nil {
		harnessError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.makeHomeFolder(email)
	s.harnessIssue(w, http.StatusCreated, harnessPerson{Email: email, Admin: body.Admin})
}

var errNoMemberRole = errors.New("no member role")

// harnessHasMemberRole reports whether lore.json defines the member role with a
// grant on some docset. `harness init` writes it; docset grants can't change
// while the server runs, so it can't be added on the fly.
func harnessHasMemberRole(auth *config.AuthConfig) bool {
	if _, ok := auth.Roles[harnessMemberRole]; !ok {
		return false
	}
	for _, docset := range auth.Docsets {
		if docset.Access.Allow[harnessMemberRole] != "" {
			return true
		}
	}
	return false
}

// harnessPersonCode issues another token for someone already on the server:
// for another of their computers, or after reinstalling. Earlier tokens keep
// working; removing the person revokes them all.
func (s *Server) harnessPersonCode(w http.ResponseWriter, r *http.Request, _ Identity) {
	email := strings.ToLower(r.PathValue("email"))
	ident, ok := s.findAuthIdentity(email)
	if !ok || !harnessEmail.MatchString(email) {
		harnessError(w, http.StatusNotFound, "no such person")
		return
	}
	s.harnessIssue(w, http.StatusOK, harnessPerson{Email: email, Admin: slices.Contains(ident.Roles, harnessAdminRole)})
}

func (s *Server) harnessIssue(w http.ResponseWriter, status int, person harnessPerson) {
	token, exp, err := s.issuer.Mint(Attribution{Principal: person.Email}, ScopeFull, harnessTokenTTL)
	if err != nil {
		harnessError(w, http.StatusInternalServerError, "minting the token: "+err.Error())
		return
	}
	harnessJSON(w, status, map[string]any{
		"person":     person,
		"token":      token,
		"expires_at": exp.UTC().Format(time.RFC3339),
		"url":        s.config.HTTPBaseURL(),
	})
}

func (s *Server) harnessRemovePerson(w http.ResponseWriter, r *http.Request, id Identity) {
	email := strings.ToLower(r.PathValue("email"))
	if strings.EqualFold(email, id.Attribution.Principal) {
		harnessError(w, http.StatusConflict, "you can't remove yourself; another admin can")
		return
	}
	found := false
	err := s.updateAuth(id.attribution(), "harness.person.remove", func(auth *config.AuthConfig) error {
		before := len(auth.Identities)
		auth.Identities = slices.DeleteFunc(auth.Identities, func(i config.AuthIdentity) bool {
			return strings.EqualFold(i.Name, email) && harnessEmail.MatchString(i.Name)
		})
		found = len(auth.Identities) != before
		return nil
	})
	if err != nil {
		harnessError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		harnessError(w, http.StatusNotFound, "no such person")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// harnessActivity lists the caller's own changes made by one of their agents
// (agent_id), newest first: what that agent wrote, exactly, from the history.
func (s *Server) harnessActivity(w http.ResponseWriter, r *http.Request, id Identity) {
	agent := r.URL.Query().Get("agent_id")
	if agent == "" {
		harnessError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	if s.history == nil {
		harnessJSON(w, http.StatusOK, map[string]any{"available": false, "entries": []any{}})
		return
	}
	page, err := s.history.Query(r.Context(), s.historyFor(id, HistoryQuery{
		Principal: id.Attribution.Principal,
		AgentID:   agent,
		Limit:     100,
		Cursor:    r.URL.Query().Get("cursor"),
	}))
	if err != nil {
		harnessError(w, http.StatusServiceUnavailable, "history unavailable")
		return
	}
	entries := make([]map[string]any, 0, len(page.Records))
	for _, record := range page.Records {
		entries = append(entries, map[string]any{"time": record.Time, "path": record.FileKey, "action": record.Action, "agent": record.Attribution.Agent, "hash": record.ContentHash})
	}
	harnessJSON(w, http.StatusOK, map[string]any{"available": true, "entries": entries, "next_cursor": page.NextCursor})
}

// harnessPasskeyLink starts a passkey registration for the caller, so they can
// sign in to the dashboard in a browser. The link works once, for 5 minutes.
func (s *Server) harnessPasskeyLink(w http.ResponseWriter, r *http.Request, id Identity) {
	if s.passkeys == nil {
		harnessError(w, http.StatusConflict, "passkeys are turned off on this server")
		return
	}
	device := strings.TrimSpace(r.URL.Query().Get("device"))
	if device == "" {
		device = "browser"
	}
	link, err := s.passkeys.RegistrationLink(s.config.HTTPBaseURL(), id.Attribution.Principal, device)
	if err != nil {
		harnessError(w, http.StatusInternalServerError, err.Error())
		return
	}
	harnessJSON(w, http.StatusOK, map[string]any{"url": link, "expires_in": 300, "dashboard": s.config.HTTPBaseURL() + "/dashboard/"})
}

func harnessJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func harnessError(w http.ResponseWriter, status int, message string) {
	harnessJSON(w, status, map[string]string{"error": message})
}
