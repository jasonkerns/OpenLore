// Package harnessbackup makes and unpacks backups of a server folder laid out by `openlore harness init`: one
// .tar.gz of openlore.yml, config/ (lore.json), data/ (the signing key and passkeys, history, audit log) and docs/,
// with a manifest. data/analytics is left out: it is only usage figures, and the server starts it again from nothing.
//
// A backup can be made while the server runs. Every document and key is replaced by an atomic rename, so each file
// is whole; history is copied before docs, and the append-only .jsonl logs are cut at their last complete line, so
// the archive is what the folder would hold had the server stopped at that moment (at worst the latest edit is in
// docs without its history entry). Extended attributes are not kept (the skills collection marker on /skills).
//
// Used by `openlore harness backup/restore` and by GET /harness/backup.
package harnessbackup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	Format   = "openlore-harness-backup"
	Version  = 1
	metaName = "MANIFEST.json"
)

// Parts are what a backup holds, relative to the folder.
var Parts = []string{"openlore.yml", "config", "data", "docs"}

// skip are left out of a backup; a restore moves them aside with the rest, so the server starts them afresh.
var skip = map[string]bool{"data/analytics": true}

type Manifest struct {
	Format  string    `json:"format"`
	Version int       `json:"version"`
	Created time.Time `json:"created"`
	Root    string    `json:"root"` // the folder it was taken from, with forward slashes
	Files   int       `json:"files"`
	Bytes   int64     `json:"bytes"`
}

// CheckLayout says whether root is laid out as harness init makes it, given the server's auth file and data folder.
func CheckLayout(root, authFile, dataDir string) error {
	if _, err := os.Stat(filepath.Join(root, "openlore.yml")); err != nil {
		return fmt.Errorf("no openlore.yml in %s (pass --dir with the folder given to harness init)", root)
	}
	if want := filepath.Join(root, "config", "lore.json"); !SamePath(authFile, want) {
		return fmt.Errorf("auth_file in openlore.yml is %s, not %s; only folders laid out by harness init can be backed up", authFile, want)
	}
	if want := filepath.Join(root, "data"); !SamePath(dataDir, want) {
		return fmt.Errorf("data_dir in openlore.yml is %s, not %s; only folders laid out by harness init can be backed up", dataDir, want)
	}
	return nil
}

func SamePath(a, b string) bool {
	a, _ = filepath.Abs(a)
	b, _ = filepath.Abs(b)
	if os.PathSeparator == '\\' {
		return strings.EqualFold(a, b) // Windows paths ignore case
	}
	return a == b
}

// transient is a file the server only has open for a moment: a write's temp file (renamed into place when done) or
// a lock file (which Windows won't let another process read while it's held).
func transient(name string) bool {
	return strings.HasPrefix(name, ".tmp-") || strings.HasPrefix(name, ".inbox_tokens-") ||
		strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".lock")
}

// Write writes a backup of root to w.
func Write(root string, w io.Writer) (Manifest, error) {
	m := Manifest{Format: Format, Version: Version, Created: time.Now().UTC(), Root: filepath.ToSlash(root)}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	add := func(rel string, info fs.FileInfo, data []byte) error {
		hdr := &tar.Header{Name: rel, Mode: int64(info.Mode().Perm()), ModTime: info.ModTime(), Format: tar.FormatPAX}
		if info.IsDir() {
			hdr.Typeflag, hdr.Name = tar.TypeDir, rel+"/"
			return tw.WriteHeader(hdr)
		}
		hdr.Typeflag, hdr.Size = tar.TypeReg, int64(len(data))
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(data)
		m.Files++
		m.Bytes += int64(len(data))
		return err
	}
	// History before docs (see the package comment); data/ again afterwards for the rest of it.
	order := []string{"openlore.yml", "config", "data/auth", "data/history", "data", "docs"}
	done := map[string]bool{}
	for _, part := range order {
		start := filepath.Join(root, filepath.FromSlash(part))
		if _, err := os.Lstat(start); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if skip[rel] {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if done[rel] {
				if d.IsDir() && rel != part {
					return fs.SkipDir
				}
				return nil
			}
			done[rel] = true
			if d.Type()&fs.ModeSymlink != 0 || !(d.IsDir() || d.Type().IsRegular()) {
				return nil // links and devices aren't part of what the server writes
			}
			if !d.IsDir() && transient(d.Name()) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if d.IsDir() {
				return add(rel, info, nil)
			}
			data, err := os.ReadFile(p)
			if errors.Is(err, fs.ErrNotExist) {
				return nil // deleted while we walked
			}
			if err != nil {
				return err
			}
			if strings.HasSuffix(rel, ".jsonl") {
				data = data[:bytes.LastIndexByte(data, '\n')+1] // a line being appended right now waits for the next backup
			}
			return add(rel, info, data)
		})
		if err != nil {
			return m, err
		}
	}
	meta, _ := json.MarshalIndent(m, "", "  ")
	if err := tw.WriteHeader(&tar.Header{Name: metaName, Mode: 0o600, Size: int64(len(meta)), ModTime: m.Created, Typeflag: tar.TypeReg}); err != nil {
		return m, err
	}
	if _, err := tw.Write(meta); err != nil {
		return m, err
	}
	if err := tw.Close(); err != nil {
		return m, err
	}
	return m, gz.Close()
}

// WriteFile writes a backup of root to dest, through a temp file in the same folder so dest is only ever whole.
func WriteFile(root, dest string) (Manifest, error) {
	if rel, err := filepath.Rel(root, dest); err == nil && !strings.HasPrefix(rel, "..") {
		for _, p := range Parts {
			if rel == p || strings.HasPrefix(filepath.ToSlash(rel), p+"/") {
				return Manifest{}, fmt.Errorf("the archive can't go inside %s, which it backs up", p)
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return Manifest{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".openlore-backup-*")
	if err != nil {
		return Manifest{}, err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	defer tmp.Close()
	m, err := Write(root, tmp)
	if err != nil {
		return m, err
	}
	if err := tmp.Chmod(0o600); err != nil && os.PathSeparator == '/' {
		return m, err
	}
	if err := tmp.Sync(); err != nil {
		return m, err
	}
	if err := tmp.Close(); err != nil {
		return m, err
	}
	return m, os.Rename(tmp.Name(), dest)
}

// Read reads an archive and returns its manifest. With root set it also unpacks it there; without, it only checks
// that the archive reads to the end and matches its manifest.
func Read(file, root string) (Manifest, error) {
	var m Manifest
	fh, err := os.Open(file)
	if err != nil {
		return m, err
	}
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		return m, fmt.Errorf("not a backup (%v)", err)
	}
	tr := tar.NewReader(gz)
	files, size := 0, int64(0)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, fmt.Errorf("damaged archive: %v", err)
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if name == metaName {
			if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&m); err != nil {
				return m, fmt.Errorf("damaged manifest: %v", err)
			}
			continue
		}
		if !entryOK(name) {
			return m, fmt.Errorf("unexpected entry %q", hdr.Name)
		}
		var data []byte
		if hdr.Typeflag == tar.TypeReg {
			if data, err = io.ReadAll(tr); err != nil {
				return m, fmt.Errorf("damaged archive: %v", err)
			}
			files++
			size += int64(len(data))
		} else if hdr.Typeflag != tar.TypeDir {
			return m, fmt.Errorf("unexpected entry %q", hdr.Name)
		}
		if root == "" {
			continue
		}
		dest := filepath.Join(root, filepath.FromSlash(name))
		perm := fs.FileMode(hdr.Mode).Perm()
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(dest, perm|0o700); err != nil {
				return m, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return m, err
		}
		if err := os.WriteFile(dest, data, perm|0o600); err != nil {
			return m, err
		}
		_ = os.Chtimes(dest, hdr.ModTime, hdr.ModTime)
	}
	if m.Format != Format {
		return m, errors.New("not an openlore harness backup (no manifest)")
	}
	if m.Version > Version {
		return m, fmt.Errorf("backup format %d is newer than this openlore understands (%d)", m.Version, Version)
	}
	if files != m.Files || size != m.Bytes {
		return m, fmt.Errorf("incomplete: %d files, %d bytes; the manifest says %d, %d", files, size, m.Files, m.Bytes)
	}
	return m, nil
}

// entryOK refuses names that would land outside the folder or outside what a backup holds.
func entryOK(name string) bool {
	if name == "" || strings.Contains(name, `\`) || strings.Contains(name, ":") || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
		return false
	}
	for _, p := range Parts {
		if name == p || strings.HasPrefix(name, p+"/") {
			return true
		}
	}
	return false
}

// MoveAside moves the parts a restore replaces into <root>/pre-restore-<time>/ and returns that folder ("" if there
// was nothing to move). data/analytics goes with data/, so the restored server starts its figures afresh.
func MoveAside(root string) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	aside := filepath.Join(root, "pre-restore-"+time.Now().UTC().Format("20060102-150405"))
	moved := false
	for _, p := range Parts {
		src := filepath.Join(root, p)
		if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if !moved {
			if err := os.Mkdir(aside, 0o700); err != nil {
				return "", err
			}
			moved = true
		}
		if err := os.Rename(src, filepath.Join(aside, p)); err != nil {
			return aside, fmt.Errorf("moving %s aside: %v", p, err)
		}
	}
	if !moved {
		return "", nil
	}
	return aside, nil
}

// RepointConfig changes paths under the folder a backup came from to the folder it was restored into.
func RepointConfig(yml, oldRoot, newRoot string) error {
	b, err := os.ReadFile(yml)
	if err != nil {
		return err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		for _, key := range []string{"auth_file:", "data_dir:"} {
			if strings.HasPrefix(strings.TrimSpace(line), key) {
				line = strings.Replace(line, oldRoot+"/", newRoot+"/", 1)
			}
		}
		out = append(out, line)
	}
	return os.WriteFile(yml, []byte(strings.Join(out, "\n")), 0o600)
}

// HumanBytes is n as bytes, KB or MB.
func HumanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
