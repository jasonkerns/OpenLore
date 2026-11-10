package openlore

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/aakarim/go-openlore/internal/harnessbackup"
)

// GET /harness/backup: an admin downloads a backup of the whole server (internal/harnessbackup), the same archive
// `openlore harness backup` writes; restore it with `openlore harness restore`. It holds everything, everyone's
// private folders and the token-signing key included, so an admin token becomes a key to all of it: the route is off
// unless the server runs with OPENLORE_BACKUP_DOWNLOAD=1, and refuses requests labelled as an agent's (harnessAuth
// never treats those as an admin's). Each download is logged with who made it.

const harnessBackupEnv = "OPENLORE_BACKUP_DOWNLOAD"

func harnessBackupEnabled() bool { return os.Getenv(harnessBackupEnv) == "1" }

func (s *Server) harnessBackup(w http.ResponseWriter, r *http.Request, id Identity) {
	if !harnessBackupEnabled() {
		harnessError(w, http.StatusForbidden, "backup download is off on this server (start it with "+harnessBackupEnv+"=1)")
		return
	}
	root := filepath.Dir(s.config.DataDir)
	if err := harnessbackup.CheckLayout(root, s.config.AuthFile, s.config.DataDir); err != nil {
		harnessError(w, http.StatusConflict, err.Error())
		return
	}
	// Written to a temp file first, so a failure is an error status rather than a cut-off download, and the
	// response carries its length.
	tmp, err := os.CreateTemp("", "openlore-backup-*.tar.gz")
	if err != nil {
		harnessError(w, http.StatusInternalServerError, "making the backup: "+err.Error())
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	m, err := harnessbackup.Write(root, tmp)
	if err != nil {
		harnessError(w, http.StatusInternalServerError, "making the backup: "+err.Error())
		return
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if err == nil {
		_, err = tmp.Seek(0, io.SeekStart)
	}
	if err != nil {
		harnessError(w, http.StatusInternalServerError, "making the backup: "+err.Error())
		return
	}
	if s.logger != nil {
		s.logger.Info("harness backup downloaded", "by", id.Attribution.String(), "files", m.Files, "bytes", size)
	}
	name := fmt.Sprintf("openlore-backup-%s.tar.gz", m.Created.Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("X-Backup-Files", strconv.Itoa(m.Files))
	w.Header().Set("X-Backup-Created", m.Created.Format(time.RFC3339))
	_, _ = io.Copy(w, tmp)
}
