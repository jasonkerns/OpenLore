package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func harnessInitForTest(t *testing.T, dir string, port int) {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := runHarness([]string{"init", "--dir", dir, "--url", "http://localhost:" + strconv.Itoa(port), "--http-port", strconv.Itoa(port)}, &out, &errOut); code != 0 {
		t.Fatalf("init exit %d: %s", code, errOut.String())
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// freePort is a port nothing listens on (at the moment of asking).
func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestHarnessBackupRestore(t *testing.T) {
	port := freePort(t)
	src := filepath.Join(t.TempDir(), "lore")
	harnessInitForTest(t, src, port)
	writeTestFile(t, filepath.Join(src, "docs", "decisions", "db.md"), "# Use Postgres\n")
	writeTestFile(t, filepath.Join(src, "data", "auth", "es256.pem"), "KEY")
	writeTestFile(t, filepath.Join(src, "data", "auth", "inbox_tokens.json.lock"), "")
	writeTestFile(t, filepath.Join(src, "data", "history", "commits.jsonl"), "{\"a\":1}\n{\"b\":") // a line being appended
	writeTestFile(t, filepath.Join(src, "data", "analytics", "aggregations.sqlite"), "figures")
	writeTestFile(t, filepath.Join(src, "docs", ".tmp-db.md-123"), "half a write")
	lore := readTestFile(t, filepath.Join(src, "config", "lore.json"))

	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	var out, errOut bytes.Buffer
	if code := runHarness([]string{"backup", "--dir", src, "--out", archive}, &out, &errOut); code != 0 {
		t.Fatalf("backup exit %d: %s", code, errOut.String())
	}

	// --check reads it without touching anything.
	out.Reset()
	dst := filepath.Join(t.TempDir(), "elsewhere")
	if code := runHarness([]string{"restore", "--from", archive, "--dir", dst, "--check"}, &out, &errOut); code != 0 {
		t.Fatalf("check exit %d: %s", code, errOut.String())
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("--check created the folder")
	}

	// Restore into another folder: same content, paths in openlore.yml moved with it.
	if code := runHarness([]string{"restore", "--from", archive, "--dir", dst}, &out, &errOut); code != 0 {
		t.Fatalf("restore exit %d: %s\n%s", code, errOut.String(), out.String())
	}
	if got := readTestFile(t, filepath.Join(dst, "docs", "decisions", "db.md")); got != "# Use Postgres\n" {
		t.Fatalf("doc = %q", got)
	}
	if got := readTestFile(t, filepath.Join(dst, "config", "lore.json")); got != lore {
		t.Fatal("lore.json differs")
	}
	if got := readTestFile(t, filepath.Join(dst, "data", "auth", "es256.pem")); got != "KEY" {
		t.Fatalf("signing key = %q", got)
	}
	if got := readTestFile(t, filepath.Join(dst, "data", "history", "commits.jsonl")); got != "{\"a\":1}\n" {
		t.Fatalf("history log = %q, want only the complete line", got)
	}
	for _, p := range []string{"data/analytics", "data/auth/inbox_tokens.json.lock", "docs/.tmp-db.md-123"} {
		if _, err := os.Stat(filepath.Join(dst, p)); !os.IsNotExist(err) {
			t.Fatalf("%s was restored", p)
		}
	}
	if _, err := harnessRoot(dst); err != nil {
		t.Fatalf("restored config doesn't point at its folder: %v", err)
	}
	if yml := readTestFile(t, filepath.Join(dst, "openlore.yml")); strings.Contains(yml, filepath.ToSlash(src)) {
		t.Fatalf("openlore.yml still names the old folder:\n%s", yml)
	}

	// Restoring over a folder moves what's there aside instead of deleting it.
	writeTestFile(t, filepath.Join(dst, "docs", "newer.md"), "after the backup")
	out.Reset()
	if code := runHarness([]string{"restore", "--from", archive, "--dir", dst}, &out, &errOut); code != 0 {
		t.Fatalf("second restore exit %d: %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dst, "docs", "newer.md")); !os.IsNotExist(err) {
		t.Fatal("newer.md survived the restore in docs/")
	}
	asides, _ := filepath.Glob(filepath.Join(dst, "pre-restore-*", "docs", "newer.md"))
	if len(asides) != 1 {
		t.Fatalf("newer.md not kept aside; output:\n%s", out.String())
	}
}

func TestHarnessRestoreRefusesWhileServerRuns(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	dir := filepath.Join(t.TempDir(), "lore")
	harnessInitForTest(t, dir, port)
	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	var out, errOut bytes.Buffer
	if code := runHarness([]string{"backup", "--dir", dir, "--out", archive}, &out, &errOut); code != 0 {
		t.Fatalf("backup exit %d: %s", code, errOut.String())
	}
	if code := runHarness([]string{"restore", "--from", archive, "--dir", dir}, &out, &errOut); code == 0 {
		t.Fatal("restore ran while something answered on the server's port")
	}
	if !strings.Contains(errOut.String(), "running") {
		t.Fatalf("stderr = %q", errOut.String())
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "pre-restore-*")); len(m) != 0 {
		t.Fatal("a refused restore moved files")
	}
}

func TestHarnessRestoreRejectsBadArchives(t *testing.T) {
	for name, entry := range map[string]string{
		"escape":     "../outside.txt",
		"absolute":   "/etc/passwd",
		"unexpected": "somewhere/else.txt",
		"backslash":  `docs\..\..\x`,
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			tw.WriteHeader(&tar.Header{Name: entry, Mode: 0o600, Size: 1, Typeflag: tar.TypeReg})
			tw.Write([]byte("x"))
			tw.Close()
			gz.Close()
			archive := filepath.Join(t.TempDir(), "bad.tar.gz")
			writeTestFile(t, archive, buf.String())
			dir := t.TempDir()
			var out, errOut bytes.Buffer
			if code := runHarness([]string{"restore", "--from", archive, "--dir", dir}, &out, &errOut); code == 0 {
				t.Fatal("restored a bad archive")
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("a rejected archive changed the folder: %v", entries)
			}
		})
	}
}

func TestHarnessBackupNeedsInitLayout(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runHarness([]string{"backup", "--dir", t.TempDir()}, &out, &errOut); code == 0 {
		t.Fatal("backed up a folder with no openlore.yml")
	}
}
