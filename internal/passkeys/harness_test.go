package passkeys

import (
	"testing"

	"github.com/aakarim/go-openlore/pkg/shell/cmds"
)

// Registering a passkey for an identity grants a browser session as it, so
// only sessions allowed admin actions may run `passkey`.
func TestPasskeyCommandIsAdminOnly(t *testing.T) {
	RegisterShellCommand(nil, "https://lore.test")
	if got := cmds.InvocationAction("passkey", []string{"register", "--identity", "owner"}); got != cmds.ActionAdmin {
		t.Fatalf("passkey register action = %q, want %q", got, cmds.ActionAdmin)
	}
}
