package openlore

import (
	"path"
	"strings"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// Private folders for Agent Harness people. Docsets can't change while the
// server runs, so instead of one docset per person there is one, "homes", at
// /home, and the folder /home/<identity> in it belongs to that identity alone:
// readableRoots shows it only its own folder (so the read-scoped filesystem
// hides everyone else's, names included), grantsForPath grants nothing in
// anyone else's (so writes and analytics stop there too), and history is scoped
// the same way (historyFor). No role, admins included, reaches into someone
// else's folder.

const (
	harnessHomesDocset = "homes"
	harnessHomesRoot   = "/home"
)

// homeFolder is id's own folder in /home ("/home/jk@test.com"), or "" when the
// server has no homes docset or id has no name to own one by. Agents work as
// their person, so they get their person's folder.
func (s *Server) homeFolder(id Identity) string {
	if _, ok := s.currentAuth().Docsets[harnessHomesDocset]; !ok {
		return ""
	}
	name := id.Principal.IdentityName
	if name == "" {
		name = id.IdentityName
	}
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return ""
	}
	return path.Join(harnessHomesRoot, name)
}

// isHomesRoot reports whether docset is the homes docset and root its root: that
// root is replaced by the identity's own folder.
func isHomesRoot(docset, root string) bool {
	return docset == harnessHomesDocset && vfs.CleanPath(root) == harnessHomesRoot
}

// ownsHomePath reports whether p, in the homes docset, is in id's own folder.
// grantsForPath grants nothing elsewhere under /home, so no reader, writer or
// report (analytics) reaches into someone else's folder, or writes to /home
// itself.
func (s *Server) ownsHomePath(id Identity, p string) bool {
	own := s.homeFolder(id)
	return own != "" && pathWithinRoot(own, s.canonicalPath(p))
}

// makeHomeFolder makes a new person's folder, as them (nobody else may write
// there). Best effort: the folder is also made on the next start, and saving a
// document makes the folders it's in.
func (s *Server) makeHomeFolder(name string) {
	person := Identity{IdentityName: name, Principal: AuthenticatedPrincipal{IdentityName: name}, Scopes: []string{ScopeFull}}
	folder := s.homeFolder(person)
	if folder == "" {
		return
	}
	if fsys, ok := s.buildCanonicalSessionFS(person).(vfs.WritableFS); ok {
		_ = fsys.MkdirAll(folder)
	}
}

// historyFor scopes a history query to what id may read: its readable roots
// (with only its own folder in /home) and every docset boundary, so carved-out
// docsets and other people's folders stay out of history and the change feed.
func (s *Server) historyFor(id Identity, query HistoryQuery) HistoryQuery {
	query.Roots = s.readableRoots(s.resolveSessionIdentity(id))
	query.Boundaries = s.allDocsetRoots()
	return query
}
