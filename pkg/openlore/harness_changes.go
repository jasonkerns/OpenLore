package openlore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// The Agent Harness review and editing API: what changed (from the commit
// journal, which records each write's before/after hashes and the new bytes,
// with pre-images kept in the blob store), any version of a file that appears
// in its history, and compare-and-swap writes and deletes through the caller's
// normal permissions and the write log (so history, rules and attribution
// apply). Everything is filtered to what the caller can read.

const harnessMaxFileBytes = 10 << 20

type harnessFileChange struct {
	Path         string `json:"path"`
	Action       string `json:"action"`
	BeforeExists bool   `json:"before_exists"`
	BeforeHash   string `json:"before_hash,omitempty"`
	AfterHash    string `json:"after_hash,omitempty"`
}

type harnessCommit struct {
	ID        string              `json:"id"`
	Time      time.Time           `json:"time"`
	By        string              `json:"by"`
	Principal string              `json:"principal"`
	Agent     string              `json:"agent,omitempty"`
	AgentID   string              `json:"agent_id,omitempty"`
	Files     []harnessFileChange `json:"files"`
}

func (s *Server) harnessChangeRoutes(mux *http.ServeMux) {
	mux.Handle("GET /harness/changes", s.harnessAuth(false, s.harnessChanges))
	mux.Handle("GET /harness/blob", s.harnessAuth(false, s.harnessBlob))
	mux.Handle("GET /harness/file", s.harnessAuth(false, s.harnessReadFile))
	mux.Handle("PUT /harness/file", s.harnessAuth(false, s.harnessWriteFile))
	mux.Handle("DELETE /harness/file", s.harnessAuth(false, s.harnessDeleteFile))
}

// harnessCommits reads the commit journal, oldest first, keeping only file
// changes under roots.
func (s *Server) harnessCommits(ctx context.Context, roots []string) ([]harnessCommit, []CommitRecord, error) {
	if s.historyPath == "" {
		return nil, nil, nil
	}
	cursor, err := OpenHistoryCursor(s.historyPath, HistoryPosition{})
	if err != nil {
		return nil, nil, err
	}
	if closer, ok := cursor.(io.Closer); ok {
		defer closer.Close()
	}
	var commits []harnessCommit
	var records []CommitRecord
	for {
		record, ok, err := cursor.Next(ctx)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			break
		}
		commit := harnessCommit{ID: record.ID, Time: record.Time, By: record.Attribution.Label(), Principal: record.Attribution.Principal, Agent: record.Attribution.Agent, AgentID: record.Attribution.AgentID}
		for _, leaf := range record.Leaves {
			if leaf.Action != vfs.ChangeActionWrite && leaf.Action != vfs.ChangeActionRemove && leaf.Action != vfs.ChangeActionRemoveAll {
				continue
			}
			if !historyReadable(roots, leaf.Target) {
				continue
			}
			commit.Files = append(commit.Files, harnessFileChange{Path: leaf.Target, Action: string(leaf.Action), BeforeExists: leaf.BeforeExists, BeforeHash: leaf.BeforeHash, AfterHash: leaf.AfterHash})
		}
		if len(commit.Files) > 0 {
			commits = append(commits, commit)
			records = append(records, record)
		}
	}
	return commits, records, nil
}

// harnessChanges lists changes newest first. `cursor` is how many to skip.
func (s *Server) harnessChanges(w http.ResponseWriter, r *http.Request, id Identity) {
	if s.historyPath == "" {
		harnessJSON(w, http.StatusOK, map[string]any{"available": false, "entries": []any{}})
		return
	}
	commits, _, err := s.harnessCommits(r.Context(), historyRoots(s.sessionDocsets(id)))
	if err != nil {
		harnessError(w, http.StatusServiceUnavailable, "history unavailable")
		return
	}
	skip, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	entries := make([]harnessCommit, 0, limit)
	for i := len(commits) - 1 - max(skip, 0); i >= 0 && len(entries) < limit; i-- {
		entries = append(entries, commits[i])
	}
	next := ""
	if max(skip, 0)+len(entries) < len(commits) {
		next = strconv.Itoa(max(skip, 0) + len(entries))
	}
	harnessJSON(w, http.StatusOK, map[string]any{"available": true, "entries": entries, "next_cursor": next, "total": len(commits)})
}

// harnessBlob returns one version of a file: only a hash that its history
// shows as a before or after of that path, so a caller can't fetch arbitrary
// objects (or another docset's) by guessing hashes.
func (s *Server) harnessBlob(w http.ResponseWriter, r *http.Request, id Identity) {
	target := s.canonicalPath(r.URL.Query().Get("path"))
	hash := r.URL.Query().Get("hash")
	if hash == "" {
		harnessError(w, http.StatusBadRequest, "path and hash are required")
		return
	}
	commits, records, err := s.harnessCommits(r.Context(), historyRoots(s.sessionDocsets(id)))
	if err != nil {
		harnessError(w, http.StatusServiceUnavailable, "history unavailable")
		return
	}
	var data []byte
	found := false
	for i, commit := range commits {
		for _, file := range commit.Files {
			if file.Path != target || (file.BeforeHash != hash && file.AfterHash != hash) {
				continue
			}
			found = true
			// New bytes are in the change itself; earlier versions are kept as pre-images.
			if file.AfterHash == hash {
				data = harnessWrittenBytes(records[i].ChangeSet, target)
			}
		}
	}
	if !found {
		harnessError(w, http.StatusNotFound, "no such version of this file")
		return
	}
	if data == nil && s.writeLog != nil && s.writeLog.blobs != nil {
		if rc, _, err := s.writeLog.blobs.Get(r.Context(), hash); err == nil {
			data, _ = io.ReadAll(io.LimitReader(rc, harnessMaxFileBytes))
			rc.Close()
		}
	}
	if data == nil {
		// The current file, if it's that version.
		if current, err := s.buildCanonicalSessionFS(id).ReadFile(target); err == nil && harnessHash(current) == hash {
			data = current
		}
	}
	if data == nil || harnessHash(data) != hash {
		harnessError(w, http.StatusGone, "that version is no longer kept")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(data)
}

func harnessWrittenBytes(cs vfs.ChangeSet, target string) []byte {
	if cs.Action == vfs.ChangeActionWrite && cs.Target == target && cs.Write != nil {
		return cs.Write.Bytes
	}
	for _, change := range cs.Changes {
		if change.Action == vfs.ChangeActionWrite && change.Target == target && change.Write != nil {
			return change.Write.Bytes
		}
	}
	return nil
}

func harnessHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// harnessReadFile returns a document's text and hash, the base for an edit.
func (s *Server) harnessReadFile(w http.ResponseWriter, r *http.Request, id Identity) {
	target := s.canonicalPath(r.URL.Query().Get("path"))
	data, err := s.buildCanonicalSessionFS(id).ReadFile(target)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
		harnessJSON(w, http.StatusOK, map[string]any{"path": target, "exists": false})
		return
	}
	if err != nil {
		harnessError(w, harnessStatus(err), "file unavailable")
		return
	}
	text := utf8.Valid(data)
	out := map[string]any{"path": target, "exists": true, "hash": harnessHash(data), "text": text}
	if text {
		out["content"] = string(data)
	}
	harnessJSON(w, http.StatusOK, out)
}

// harnessWriteFile replaces (If-Match: <hash>) or creates (If-None-Match: *) a
// document. One of the two is required: writes from the harness never
// overwrite blindly.
func (s *Server) harnessWriteFile(w http.ResponseWriter, r *http.Request, id Identity) {
	target := s.canonicalPath(r.URL.Query().Get("path"))
	if target == "/" || strings.HasSuffix(r.URL.Query().Get("path"), "/") {
		harnessError(w, http.StatusBadRequest, "a document path is required")
		return
	}
	var opts vfs.WriteOpts
	if match := strings.Trim(r.Header.Get("If-Match"), `" `); match != "" {
		opts.IfMatch = &match
	} else if r.Header.Get("If-None-Match") == "*" {
		opts.IfNoneMatch = true
	} else {
		harnessError(w, http.StatusPreconditionRequired, "send If-Match with the hash you edited, or If-None-Match: * to create")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, harnessMaxFileBytes))
	if err != nil {
		harnessError(w, http.StatusRequestEntityTooLarge, "documents are limited to 10 MiB")
		return
	}
	writable, ok := s.buildCanonicalSessionFS(id).(vfs.WritableFS)
	if !ok {
		harnessError(w, http.StatusForbidden, "this knowledge base is read-only for you")
		return
	}
	if opts.IfNoneMatch {
		if dir := path.Dir(target); dir != "/" {
			if err := writable.MkdirAll(dir); err != nil && !errors.Is(err, fs.ErrExist) {
				harnessError(w, harnessStatus(err), "can't create the folder: "+err.Error())
				return
			}
		}
	}
	hash, err := writable.WriteFileAtomic(target, data, opts)
	var stale *vfs.PreconditionError
	if opts.IfNoneMatch && errors.As(err, &stale) {
		harnessJSON(w, http.StatusPreconditionFailed, map[string]any{"error": "a document already exists at that path", "current": stale.Current})
		return
	}
	if err != nil {
		s.harnessWriteError(w, err)
		return
	}
	harnessJSON(w, http.StatusOK, map[string]any{"path": target, "hash": hash})
}

// harnessDeleteFile removes a document if it's still the version (If-Match)
// the caller saw.
func (s *Server) harnessDeleteFile(w http.ResponseWriter, r *http.Request, id Identity) {
	target := s.canonicalPath(r.URL.Query().Get("path"))
	match := strings.Trim(r.Header.Get("If-Match"), `" `)
	if match == "" {
		harnessError(w, http.StatusPreconditionRequired, "send If-Match with the hash of the version to delete")
		return
	}
	fsys := s.buildCanonicalSessionFS(id)
	writable, ok := fsys.(vfs.WritableFS)
	if !ok {
		harnessError(w, http.StatusForbidden, "this knowledge base is read-only for you")
		return
	}
	current, err := fsys.ReadFile(target)
	if err != nil {
		harnessError(w, harnessStatus(err), "file unavailable")
		return
	}
	if harnessHash(current) != match {
		harnessJSON(w, http.StatusPreconditionFailed, map[string]any{"error": "the file changed since you saw it", "current": harnessHash(current)})
		return
	}
	if err := writable.Remove(target); err != nil {
		s.harnessWriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) harnessWriteError(w http.ResponseWriter, err error) {
	var stale *vfs.PreconditionError
	if errors.As(err, &stale) {
		harnessJSON(w, http.StatusPreconditionFailed, map[string]any{"error": "the file changed since you saw it", "current": stale.Current})
		return
	}
	harnessError(w, harnessStatus(err), err.Error())
}

func harnessStatus(err error) int {
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, os.ErrNotExist):
		return http.StatusNotFound
	case errors.Is(err, fs.ErrPermission), errors.Is(err, os.ErrPermission):
		return http.StatusForbidden
	default:
		if msg := strings.ToLower(err.Error()); strings.Contains(msg, "read-only") || strings.Contains(msg, "readonly") {
			return http.StatusForbidden
		}
		return http.StatusInternalServerError
	}
}
