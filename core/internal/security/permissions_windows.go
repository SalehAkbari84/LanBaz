//go:build windows

package security

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// replaceFile atomically moves src onto dst. os.Rename already maps to
// MoveFileEx on Windows, which fails when the destination exists unless the
// replace flag is used, hence the explicit implementation.
func replaceFile(src, dst string) error {
	from, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// RestrictToOwner applies an owner-only DACL to path. On Windows the portable
// 0600 mode is ignored, so the ACL is what actually keeps other users on the
// machine from reading the API token out of the state file.
func RestrictToOwner(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	sid, err := currentUserSID()
	if err != nil {
		return err
	}

	// Owner + SYSTEM + Administrators, protected so that inherited entries
	// cannot re-add access for other principals.
	sddl := "D:P(A;;FA;;;" + sid.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}

	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	return windows.SetNamedSecurityInfo(abs, windows.SE_FILE_OBJECT, info, nil, nil, dacl, nil)
}

func currentUserSID() (*windows.SID, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}
