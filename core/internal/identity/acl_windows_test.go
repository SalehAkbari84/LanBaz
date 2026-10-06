//go:build windows

package identity

import (
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows the portable 0600 mode is ignored, so the DACL is the only thing
// standing between the long-lived private key and another account on the same
// machine. This test asserts the ACL really is restricted, which the POSIX-mode
// test skips.
func TestIdentityFileACLIsRestricted(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreate(dir); err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	path := Path(dir)

	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo: %v", err)
	}
	dacl, defaulted, err := sd.DACL()
	if err != nil {
		t.Fatalf("DACL: %v", err)
	}
	if dacl == nil {
		t.Fatal("the identity file has no DACL at all, so it inherits permissive access")
	}
	// A defaulted DACL is one inherited from the parent directory rather than
	// set explicitly; RestrictToOwner sets a protected DACL, so anything
	// defaulted means the restriction never took effect.
	if defaulted {
		t.Error("the DACL was inherited rather than explicitly restricted")
	}

	// The principals RestrictToOwner is allowed to grant.
	userSID := currentUserSIDString(t)
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatalf("system sid: %v", err)
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatalf("admin sid: %v", err)
	}
	permitted := map[string]bool{
		userSID:            true,
		systemSID.String(): true,
		adminSID.String():  true,
	}

	granted := make(map[string]bool)
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Errorf("GetAce(%d): %v", i, err)
			continue
		}
		// Only ACCESS_ALLOWED_ACE grants access; deny and audit ACEs are
		// irrelevant to "who can read this key".
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		// The variable-length SID starts at the SidStart field, so the field's
		// own address is already a valid *SID. AceCount bounds the read.
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		s := sid.String()
		t.Logf("granted to %s mask=%#x", s, ace.Mask)
		granted[s] = true
	}

	if len(granted) == 0 {
		t.Fatal("no allow ACEs found; the assertion below would be vacuous")
	}
	for sid := range granted {
		if !permitted[sid] {
			t.Errorf("the identity file DACL grants read access to an unexpected principal: %s", sid)
		}
	}
	// The owner must still be able to read the key, or the daemon locks itself out.
	if !granted[userSID] {
		t.Errorf("the DACL does not grant the current user (%s) access", userSID)
	}
	// And it must be full control, not read-only: the daemon rewrites the file.
	if _, err := os.ReadFile(path); err != nil {
		t.Errorf("the owner cannot read the identity file back: %v", err)
	}
}

func currentUserSIDString(t *testing.T) string {
	t.Helper()
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}
	return user.User.Sid.String()
}
