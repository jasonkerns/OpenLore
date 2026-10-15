package openlore

import (
	"os"
	"time"
)

// authWatchInterval is how often lore.json is checked for changes made outside
// the server (by an editor, or by an app managing identities).
const authWatchInterval = 2 * time.Second

// startAuthWatch reloads lore.json whenever it changes on disk, so adding or
// removing an identity or delegate takes effect without a restart. A reload
// that fails validation (including one that would need a restart) keeps the
// current config and is recorded in the audit log as config.reject, exactly as
// `lore config reload` does.
func (s *Server) startAuthWatch() {
	if s.config.AuthFile == "" || s.authWatchStop != nil {
		return
	}
	stop := make(chan struct{})
	s.authWatchStop = stop
	stamp := func() (time.Time, int64) {
		info, err := os.Stat(s.config.AuthFile)
		if err != nil {
			return time.Time{}, -1
		}
		return info.ModTime(), info.Size()
	}
	lastMod, lastSize := stamp()
	go func() {
		ticker := time.NewTicker(authWatchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				mod, size := stamp()
				if size < 0 || (mod.Equal(lastMod) && size == lastSize) {
					continue
				}
				lastMod, lastSize = mod, size
				if err := s.reloadAuth(Attribution{Principal: "system"}); err != nil {
					s.logger.Warn("lore.json changed but was not reloaded", "error", err)
				} else {
					s.logger.Info("lore.json reloaded")
				}
			}
		}
	}()
}

func (s *Server) stopAuthWatch() {
	if s.authWatchStop != nil {
		close(s.authWatchStop)
		s.authWatchStop = nil
	}
}
