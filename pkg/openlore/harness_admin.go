package openlore

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
)

// The Agent Harness admin API. Each computer running Agent Harness is an
// identity named machine-<name> that is a delegate of the owner, so its writes
// are recorded as the owner acting through that machine. An admin machine (one
// whose delegate entry does not deny lore:config:edit) manages the others from
// the app: it adds machines (receiving their long-lived tokens), removes them
// (which revokes those tokens), and gets passkey links for the owner's browser.
//
// Every route needs a bearer token; the management routes also need
// lore:config:edit, the same capability that guards lore.json in the shell.

const (
	harnessMachinePrefix = "machine-"
	harnessMachineTTL    = 365 * 24 * time.Hour
	harnessConfigEdit    = "lore:config:edit"
)

var harnessMachineName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

type harnessMachine struct {
	Name     string `json:"name"`
	Identity string `json:"identity"`
	Admin    bool   `json:"admin"`
}

func (s *Server) harnessRoutes() HTTPRouteRegistrar {
	return func(mux *http.ServeMux) {
		// For load balancer health checks; says nothing about the server.
		mux.HandleFunc("GET /harness/health", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte("ok\n"))
		})
		mux.Handle("GET /harness/me", s.harnessAuth(false, s.harnessMe))
		mux.Handle("GET /harness/machines", s.harnessAuth(true, s.harnessListMachines))
		mux.Handle("POST /harness/machines", s.harnessAuth(true, s.harnessAddMachine))
		mux.Handle("DELETE /harness/machines/{name}", s.harnessAuth(true, s.harnessRemoveMachine))
		mux.Handle("POST /harness/passkey", s.harnessAuth(true, s.harnessPasskeyLink))
	}
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
		if admin && !s.harnessIsAdmin(id) {
			harnessError(w, http.StatusForbidden, "this machine can't manage the knowledge base")
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

func (s *Server) harnessListMachines(w http.ResponseWriter, _ *http.Request, id Identity) {
	principal, ok := s.findAuthIdentity(id.Attribution.Principal)
	if !ok {
		harnessError(w, http.StatusForbidden, "unknown principal")
		return
	}
	machines := []harnessMachine{}
	for _, d := range principal.Delegates {
		if name, ok := strings.CutPrefix(d.Identity, harnessMachinePrefix); ok {
			machines = append(machines, harnessMachine{Name: name, Identity: d.Identity, Admin: !slices.Contains(d.DenyCapabilities, harnessConfigEdit)})
		}
	}
	harnessJSON(w, http.StatusOK, map[string]any{"machines": machines})
}

func (s *Server) harnessAddMachine(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		Name  string `json:"name"`
		Admin bool   `json:"admin"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		harnessError(w, http.StatusBadRequest, "expected JSON {\"name\": ..., \"admin\": ...}")
		return
	}
	if !harnessMachineName.MatchString(body.Name) {
		harnessError(w, http.StatusBadRequest, "a machine name is 1-40 lowercase letters, digits and dashes")
		return
	}
	principalName := id.Attribution.Principal
	identity := harnessMachinePrefix + body.Name
	errExists := errors.New("exists")
	err := s.updateAuth(id.attribution(), "harness.machine.add", func(auth *config.AuthConfig) error {
		for _, existing := range auth.Identities {
			if existing.Name == identity {
				return errExists
			}
		}
		for i := range auth.Identities {
			if auth.Identities[i].Name != principalName {
				continue
			}
			entry := config.DelegateEntry{Identity: identity}
			if !body.Admin {
				entry.DenyCapabilities = []string{harnessConfigEdit}
			}
			auth.Identities[i].Delegates = append(auth.Identities[i].Delegates, entry)
			auth.Identities = append(auth.Identities, config.AuthIdentity{Name: identity, Comment: "Agent Harness machine"})
			return nil
		}
		return fmt.Errorf("principal %q is not in lore.json", principalName)
	})
	if errors.Is(err, errExists) {
		harnessError(w, http.StatusConflict, fmt.Sprintf("a machine named %q already exists; remove it first or pick another name", body.Name))
		return
	}
	if err != nil {
		harnessError(w, http.StatusInternalServerError, err.Error())
		return
	}
	token, exp, err := s.issuer.Mint(Attribution{Principal: principalName, Actor: identity}, ScopeFull, harnessMachineTTL)
	if err != nil {
		harnessError(w, http.StatusInternalServerError, "minting the machine's token: "+err.Error())
		return
	}
	harnessJSON(w, http.StatusCreated, map[string]any{
		"machine":    harnessMachine{Name: body.Name, Identity: identity, Admin: body.Admin},
		"token":      token,
		"expires_at": exp.UTC().Format(time.RFC3339),
		"url":        s.config.HTTPBaseURL(),
	})
}

func (s *Server) harnessRemoveMachine(w http.ResponseWriter, r *http.Request, id Identity) {
	identity := harnessMachinePrefix + r.PathValue("name")
	if identity == id.Attribution.Actor {
		harnessError(w, http.StatusConflict, "a machine can't remove itself; remove it from another admin machine")
		return
	}
	principalName := id.Attribution.Principal
	found := false
	err := s.updateAuth(id.attribution(), "harness.machine.remove", func(auth *config.AuthConfig) error {
		for i := range auth.Identities {
			if auth.Identities[i].Name == principalName {
				before := len(auth.Identities[i].Delegates)
				auth.Identities[i].Delegates = slices.DeleteFunc(auth.Identities[i].Delegates, func(d config.DelegateEntry) bool { return d.Identity == identity })
				found = found || len(auth.Identities[i].Delegates) != before
			}
		}
		before := len(auth.Identities)
		auth.Identities = slices.DeleteFunc(auth.Identities, func(i config.AuthIdentity) bool { return i.Name == identity })
		found = found || len(auth.Identities) != before
		return nil
	})
	if err != nil {
		harnessError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		harnessError(w, http.StatusNotFound, "no such machine")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// harnessPasskeyLink starts a passkey registration for the owner, so the owner
// can sign in to the dashboard in a browser. The link works once, for 5 minutes.
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
