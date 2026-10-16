package openlore

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// harnessMakeFolder creates an empty folder (and any folders above it): POST
// /harness/folder {"path": "/docs/new"}. Saving a document makes the folders
// it's in, but the Knowledge view's New folder makes one before anything is in
// it. It goes through the caller's own filesystem, so the usual write rules
// decide, private folders in /home included. 201 when made, 200 when it's
// already there, 409 when a document has that path.
func (s *Server) harnessMakeFolder(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || strings.TrimSpace(body.Path) == "" {
		harnessError(w, http.StatusBadRequest, `expected JSON {"path": "/folder/name"}`)
		return
	}
	target := s.canonicalPath(body.Path)
	if target == "/" {
		harnessError(w, http.StatusBadRequest, "a folder path is required")
		return
	}
	fsys := s.buildCanonicalSessionFS(id)
	if info, err := fsys.Stat(target); err == nil {
		if !info.Dir {
			harnessError(w, http.StatusConflict, "a document already has that path")
			return
		}
		harnessJSON(w, http.StatusOK, map[string]any{"path": target, "created": false})
		return
	}
	writable, ok := fsys.(vfs.WritableFS)
	if !ok {
		harnessError(w, http.StatusForbidden, "this knowledge base is read-only for you")
		return
	}
	if err := writable.MkdirAll(target); err != nil {
		s.harnessWriteError(w, err)
		return
	}
	harnessJSON(w, http.StatusCreated, map[string]any{"path": target, "created": true})
}
