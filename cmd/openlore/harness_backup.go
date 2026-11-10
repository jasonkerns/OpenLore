package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/internal/harnessbackup"
)

// Backups of a server set up with `openlore harness init` (what's in one: internal/harnessbackup):
//
//	openlore harness backup  [--dir <dir>] [--out <file.tar.gz>]
//	openlore harness restore --from <file.tar.gz> [--dir <dir>] [--check] [--force]
//
// backup can run while the server does. restore needs it stopped: it moves what is there now to
// <dir>/pre-restore-<time>/ (nothing is deleted), then unpacks the archive. If the backup came from another folder,
// the paths in openlore.yml are changed to this one. Admins can also download a backup from a running server
// (GET /harness/backup, when OPENLORE_BACKUP_DOWNLOAD=1).

func harnessBackup(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("harness backup", flag.ContinueOnError)
	f.SetOutput(stderr)
	dir := f.String("dir", ".", "the server's folder (the --dir given to harness init)")
	out := f.String("out", "", "the archive to write (default ./openlore-backup-<time>.tar.gz)")
	if err := f.Parse(args); err != nil {
		return 2
	}
	root, err := harnessRoot(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	dest := *out
	if dest == "" {
		dest = "openlore-backup-" + time.Now().UTC().Format("20060102-150405") + ".tar.gz"
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	m, err := harnessbackup.WriteFile(root, dest)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Backed up %d files (%s) from %s to\n  %s\n", m.Files, harnessbackup.HumanBytes(m.Bytes), root, dest)
	fmt.Fprintln(stdout, "It holds the key that signs everyone's tokens: keep it as safe as the server.")
	return 0
}

func harnessRestore(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("harness restore", flag.ContinueOnError)
	f.SetOutput(stderr)
	dir := f.String("dir", ".", "the server's folder to restore into")
	from := f.String("from", "", "the backup archive (required)")
	check := f.Bool("check", false, "only read the archive and say what's in it; change nothing")
	force := f.Bool("force", false, "restore even though something answers on the server's port")
	if err := f.Parse(args); err != nil {
		return 2
	}
	if *from == "" {
		fmt.Fprintln(stderr, "error: --from <backup .tar.gz> is required")
		return 2
	}
	m, err := harnessbackup.Read(*from, "")
	if err != nil {
		fmt.Fprintf(stderr, "error: %s: %v\n", *from, err)
		return 1
	}
	fmt.Fprintf(stdout, "Backup of %s, taken %s: %d files (%s).\n", m.Root, m.Created.Local().Format(time.RFC1123), m.Files, harnessbackup.HumanBytes(m.Bytes))
	if *check {
		return 0
	}
	root, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if port, up := serverAnswering(root); up && !*force {
		fmt.Fprintf(stderr, "error: something answers on port %d, so the server looks to be running. Stop it first (on AWS: desired tasks 0), or pass --force if that isn't it.\n", port)
		return 1
	}
	aside, err := harnessbackup.MoveAside(root)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if aside != "" {
		fmt.Fprintf(stdout, "Moved what was there to %s\n", aside)
	}
	if _, err := harnessbackup.Read(*from, root); err != nil {
		fmt.Fprintf(stderr, "error: unpacking: %v\n", err)
		if aside != "" {
			fmt.Fprintf(stderr, "The folder is partly restored; what was there before is in %s.\n", aside)
		}
		return 1
	}
	if old := strings.TrimRight(m.Root, "/"); old != filepath.ToSlash(root) {
		if err := harnessbackup.RepointConfig(filepath.Join(root, "openlore.yml"), old, filepath.ToSlash(root)); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Changed the paths in openlore.yml from %s to %s\n", old, filepath.ToSlash(root))
	}
	if _, err := harnessRoot(root); err != nil {
		fmt.Fprintf(stderr, "error: restored, but the config doesn't fit this folder: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Restored into %s. Start the server.\n", root)
	return 0
}

// harnessRoot checks that dir is laid out as harness init makes it and returns its absolute path.
func harnessRoot(dir string) (string, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	yml := filepath.Join(root, "openlore.yml")
	if _, err := os.Stat(yml); err != nil {
		return "", fmt.Errorf("no openlore.yml in %s (pass --dir with the folder given to harness init)", root)
	}
	cfg, err := config.New(config.WithConfigFile(yml))
	if err != nil {
		return "", fmt.Errorf("loading %s: %v", yml, err)
	}
	return root, harnessbackup.CheckLayout(root, cfg.AuthFile, cfg.DataDir)
}

// serverAnswering says whether something accepts connections on the HTTP port in the folder's openlore.yml.
func serverAnswering(root string) (int, bool) {
	b, err := os.ReadFile(filepath.Join(root, "openlore.yml"))
	if err != nil {
		return 0, false
	}
	port := 8080
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "http_port:"); ok {
			if p, err := strconv.Atoi(strings.TrimSpace(strings.SplitN(v, "#", 2)[0])); err == nil {
				port = p
			}
		}
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond)
	if err != nil {
		return port, false
	}
	c.Close()
	return port, true
}
