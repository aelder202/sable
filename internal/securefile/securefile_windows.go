//go:build windows

package securefile

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func restrictPlatform(path string) error {
	sids, err := allowedSIDs()
	if err != nil {
		return err
	}
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(sids))
	for _, sid := range sids {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	)
}

func permissionWarningPlatform(path string) (string, error) {
	allowed, err := allowedSIDSet()
	if err != nil {
		return "", err
	}
	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return "", err
	}

	var warnings []string
	control, _, err := sd.Control()
	if err == nil && control&windows.SE_DACL_PROTECTED == 0 {
		warnings = append(warnings, "inherits parent ACLs")
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		return "", err
	}
	if dacl == nil {
		warnings = append(warnings, "has no explicit DACL")
	} else {
		for _, sid := range allowedACEs(dacl) {
			if allowed[sid.String()] {
				continue
			}
			warnings = append(warnings, "grants access to "+accountName(sid))
		}
	}

	if len(warnings) == 0 {
		return "", nil
	}
	return fmt.Sprintf("%s %s", path, strings.Join(warnings, "; ")), nil
}

func allowedSIDs() ([]*windows.SID, error) {
	current, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	return uniqueSIDs([]*windows.SID{current, admins, system}), nil
}

func allowedSIDSet() (map[string]bool, error) {
	sids, err := allowedSIDs()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(sids))
	for _, sid := range sids {
		out[sid.String()] = true
	}
	return out, nil
}

func uniqueSIDs(sids []*windows.SID) []*windows.SID {
	seen := make(map[string]bool, len(sids))
	out := make([]*windows.SID, 0, len(sids))
	for _, sid := range sids {
		if sid == nil {
			continue
		}
		key := sid.String()
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, sid)
	}
	return out
}

func currentUserSID() (*windows.SID, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func allowedACEs(acl *windows.ACL) []*windows.SID {
	var out []*windows.SID
	for i := uint16(0); i < acl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, uint32(i), &ace); err != nil || ace == nil {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask == 0 {
			continue
		}
		out = append(out, (*windows.SID)(unsafe.Pointer(&ace.SidStart)))
	}
	return out
}

func accountName(sid *windows.SID) string {
	if sid == nil {
		return "unknown SID"
	}
	account, domain, _, err := sid.LookupAccount("")
	if err != nil || account == "" {
		return sid.String()
	}
	if domain == "" {
		return account
	}
	return domain + `\` + account
}
