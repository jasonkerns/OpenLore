package openlore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	mux.Handle("POST /harness/move", s.harnessAuth(false, s.harnessMove))
	mux.Handle("POST /harness/delete", s.harnessAuth(false, s.harnessDelete))
}

// harnessCommits reads the commit journal, oldest first, keeping only file
// changes under roots.
func (s *Server) harnessCommits(ctx context.Context, scope HistoryQuery) ([]harnessCommit, []CommitRecord, error) {
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
			if !historyReadable(scope.Roots, scope.Boundaries, leaf.Target) {
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
	commits, _, err := s.harnessCommits(r.Context(), s.historyFor(id, HistoryQuery{}))
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
	commits, records, err := s.harnessCommits(r.Context(), s.historyFor(id, HistoryQuery{}))
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

// Limits on one move or delete, which commits as a single batch.
const (
	harnessMaxMoveFiles = 2000
	harnessMaxMoveBytes = 100 << 20
)

var errHarnessMoveTooBig = errors.New("too much to change at once")

// harnessTreeBatch builds the batch that removes from (a document, or a folder
// and everything in it) and, when to is set, first writes it all there (each
// file's write paired with its removal as a move). Every removal is
// compare-and-swap on what was read, and emptied folders go last, deepest
// first, so a folder holding anything this caller can't see stays and the
// whole batch rolls back.
func harnessTreeBatch(fsys vfs.FileSystem, from, to string, dir bool) (vfs.ChangeSet, int, error) {
	var changes []vfs.Change
	var moves []vfs.Move
	var folders []string
	files, size := 0, 0
	var walk func(src, dst string, dir bool) error
	walk = func(src, dst string, dir bool) error {
		if dir {
			if to != "" {
				changes = append(changes, vfs.Change{Target: dst, Action: vfs.ChangeActionMkdirAll})
			}
			entries, err := fsys.ReadDir(src)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if err := walk(path.Join(src, entry.Name()), path.Join(dst, entry.Name()), entry.IsDir()); err != nil {
					return err
				}
			}
			folders = append(folders, src)
			return nil
		}
		data, err := fsys.ReadFile(src)
		if err != nil {
			return err
		}
		files++
		size += len(data)
		if files > harnessMaxMoveFiles || size > harnessMaxMoveBytes {
			return errHarnessMoveTooBig
		}
		if to != "" {
			changes = append(changes, vfs.Change{Target: dst, Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: data, Opts: vfs.WriteOpts{IfNoneMatch: true}}})
		}
		snapshot := &vfs.TreeSnapshot{Root: src, Ops: []vfs.TreeOp{{RelPath: ".", Kind: "file", Hash: harnessHash(data), Size: int64(len(data))}}}
		changes = append(changes, vfs.Change{Target: src, Action: vfs.ChangeActionRemoveAll, RemoveAll: &vfs.RemoveAllChange{Opts: vfs.RemoveOpts{Expected: snapshot}}})
		if to != "" {
			moves = append(moves, vfs.Move{From: len(changes) - 1, To: len(changes) - 2})
		}
		return nil
	}
	if parent := path.Dir(to); to != "" && !dir && parent != "/" {
		changes = append(changes, vfs.Change{Target: parent, Action: vfs.ChangeActionMkdirAll})
	}
	if err := walk(from, to, dir); err != nil {
		return vfs.ChangeSet{}, 0, err
	}
	for _, folder := range folders {
		changes = append(changes, vfs.Change{Target: folder, Action: vfs.ChangeActionRemove})
	}
	return vfs.ChangeSet{Changes: changes, Moves: moves}, files, nil
}

// harnessMove moves a document, or a folder and everything in it, to a new
// path (`{"from": ..., "to": ...}`, `to` being the new path itself, not the
// folder it goes in), so it renames too. It commits as one batch through the
// caller's normal permissions and the write log; if any part fails nothing
// changes. It never overwrites.
func (s *Server) harnessMove(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil || body.From == "" || body.To == "" {
		harnessError(w, http.StatusBadRequest, "from and to are required")
		return
	}
	from, to := s.canonicalPath(body.From), s.canonicalPath(body.To)
	if from == "/" || to == "/" {
		harnessError(w, http.StatusBadRequest, "the workspace itself can't be moved or replaced")
		return
	}
	if from == to {
		harnessJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "files": 0})
		return
	}
	if strings.HasPrefix(to, from+"/") {
		harnessError(w, http.StatusBadRequest, "a folder can't move inside itself")
		return
	}
	fsys := s.buildCanonicalSessionFS(id)
	info, err := fsys.Stat(from)
	if err != nil {
		harnessError(w, harnessStatus(err), "nothing to move at "+from)
		return
	}
	if _, err := fsys.Stat(to); err == nil {
		harnessJSON(w, http.StatusPreconditionFailed, map[string]any{"error": "something already exists at " + to})
		return
	}
	s.harnessCommitTree(w, fsys, from, to, info.Dir, map[string]any{"from": from, "to": to})
}

// harnessDelete deletes a document, or a folder and everything in it
// (`{"path": ...}`), as one batch: all of it or, if anything fails or changed
// meanwhile, none of it.
func (s *Server) harnessDelete(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil || body.Path == "" {
		harnessError(w, http.StatusBadRequest, "path is required")
		return
	}
	target := s.canonicalPath(body.Path)
	if target == "/" {
		harnessError(w, http.StatusBadRequest, "the workspace itself can't be deleted")
		return
	}
	fsys := s.buildCanonicalSessionFS(id)
	info, err := fsys.Stat(target)
	if err != nil {
		harnessError(w, harnessStatus(err), "nothing to delete at "+target)
		return
	}
	s.harnessCommitTree(w, fsys, target, "", info.Dir, map[string]any{"path": target})
}

// harnessCommitTree builds and admits a move (to set) or delete batch, and answers with out plus the file count.
func (s *Server) harnessCommitTree(w http.ResponseWriter, fsys vfs.FileSystem, from, to string, dir bool, out map[string]any) {
	admitter, ok := fsys.(vfs.ChangeSetAdmitter)
	if !ok {
		harnessError(w, http.StatusForbidden, "this knowledge base is read-only for you")
		return
	}
	cs, files, err := harnessTreeBatch(fsys, from, to, dir)
	if err != nil {
		if errors.Is(err, errHarnessMoveTooBig) {
			harnessError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("one change is limited to %d files and %d MiB", harnessMaxMoveFiles, harnessMaxMoveBytes>>20))
		} else {
			harnessError(w, harnessStatus(err), "can't read "+from+": "+err.Error())
		}
		return
	}
	out["files"] = files
	err = admitter.AdmitChangeSet(cs)
	var pending *vfs.PendingChangeError
	var stale *vfs.TreeStaleError
	var exists *vfs.PreconditionError
	switch {
	case err == nil:
		harnessJSON(w, http.StatusOK, out)
	case errors.As(err, &pending):
		out["pending"] = pending.Ref
		harnessJSON(w, http.StatusAccepted, out)
	case errors.As(err, &stale):
		harnessJSON(w, http.StatusPreconditionFailed, map[string]any{"error": "something changed meanwhile; nothing was changed"})
	case errors.As(err, &exists) && to != "":
		harnessJSON(w, http.StatusPreconditionFailed, map[string]any{"error": "something already exists at " + to})
	default:
		harnessError(w, harnessStatus(err), err.Error())
	}
}

// harnessWritable reports whether id may move, rename or delete the entry at
// p: never a docset root or anything above one, otherwise the caller's normal
// write permission there.
func (s *Server) harnessWritable(id Identity, p string) bool {
	if auth := s.currentAuth(); auth != nil {
		for _, ds := range auth.Docsets {
			for _, pm := range ds.Paths {
				if pathWithinRoot(p, displayPath(pm)) { // p is the root or above it
					return false
				}
			}
		}
	}
	if !s.authEnforced {
		return !s.config.Readonly
	}
	return s.identityCanWrite(id, vfs.ChangeActionRemove, p)
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
