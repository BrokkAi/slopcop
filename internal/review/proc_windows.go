//go:build windows

package review

import (
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// trustedInstallerSID owns the system drive's root on a default install.
const trustedInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

// Deny entries that x/sys/windows has no name for.
const (
	accessDeniedObjectACEType         = 6
	accessDeniedCallbackACEType       = 10
	accessDeniedCallbackObjectACEType = 12
)

// configureProcess prepares the adapter for whole-tree cancellation.
func configureProcess(cmd *exec.Cmd) {}

// killProcessTree stops the adapter and descendants on timeout or cancellation.
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		_ = cmd.Process.Kill()
	}
}

// writableByOthers reports whether another user can add anything to path or
// take it over, from its owner and access list.
func writableByOthers(path string) (bool, error) {
	ownerTrusted, grants, err := namedAccess(path)
	if err != nil {
		return false, err
	}
	return othersMayPlant(ownerTrusted, grants) || othersMayAddFolders(grants), nil
}

// passesWriteToNewEntries reports whether dir gives another account write
// access to what is created in it, from the moment it exists.
func passesWriteToNewEntries(dir string) (bool, error) {
	_, grants, err := namedAccess(dir)
	if err != nil {
		return false, err
	}
	return othersInherit(grants), nil
}

// protectParent reports whether another user can plant files in dir. Where
// others can only add folders, as in the root of the system drive, it makes
// sure the one folder npm and Node would use is already taken by a locked one.
func protectParent(dir string) (bool, error) {
	ownerTrusted, grants, err := namedAccess(dir)
	if err != nil {
		return false, err
	}
	if othersMayPlant(ownerTrusted, grants) {
		return true, nil
	}
	if !othersMayAddFolders(grants) {
		return false, nil
	}
	return false, lockNodeModules(filepath.Join(dir, "node_modules"))
}

// lockNodeModules creates path with an access list only this user, SYSTEM and
// Administrators can write through. The list is set as the folder is created,
// so there is no moment another user could add to it. A folder already there
// must be a real directory that no other account can write to.
func lockNodeModules(path string) error {
	me, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("read the current user: %w", err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + me.User.Sid.String() + ")(A;OICI;GRGX;;;AU)")
	if err != nil {
		return fmt.Errorf("build the access list for %s: %w", path, err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs := windows.SecurityAttributes{SecurityDescriptor: sd}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	err = windows.CreateDirectory(name, &attrs)
	if err == nil {
		return nil
	}
	if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("create %s: %w", path, err)
	}
	return verifyLockedNodeModules(path)
}

// verifyLockedNodeModules checks an existing folder and everything in it,
// since a package another account can change is as unsafe as the folder.
func verifyLockedNodeModules(path string) error {
	isDir, err := verifyLockedEntry(path)
	if err != nil {
		return err
	}
	if !isDir {
		return changeableError(path)
	}
	return filepath.WalkDir(path, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == path {
			return nil
		}
		_, err = verifyLockedEntry(p)
		return err
	})
}

// verifyLockedEntry reads one entry through a handle that does not follow a
// junction, so what is checked is what npm would find there. It reports
// whether the entry is a directory.
func verifyLockedEntry(path string) (bool, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(h, &info); err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false, changeableError(path)
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, fmt.Errorf("read the access list of %s: %w", path, err)
	}
	ownerTrusted, grants, err := readAccess(sd)
	if err != nil {
		return false, fmt.Errorf("read the access list of %s: %w", path, err)
	}
	if othersMayPlant(ownerTrusted, grants) || othersMayAddFolders(grants) {
		return false, changeableError(path)
	}
	return info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0, nil
}

func changeableError(path string) error {
	return fmt.Errorf("%s can be changed by another account, which could change what the reviewer runs; see https://docs.slopcop.com/checks/run-a-check/#local-review-on-windows", path)
}

// protectCommandParents locks node_modules in the directories above a Node
// command's install directory where others can add to them, because Node
// searches those for npm's own modules. The install directory itself holds
// npm's real node_modules and is left alone.
func protectCommandParents(command string) error {
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(command), filepath.Ext(command)))
	if name != "npx" && name != "npm" && name != "node" {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(command)
	if err != nil {
		return err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return err
	}
	for p := filepath.Dir(filepath.Dir(resolved)); ; p = filepath.Dir(p) {
		ownerTrusted, grants, err := namedAccess(p)
		if err != nil {
			return err
		}
		if othersMayReplaceEntries(ownerTrusted, grants) {
			return fmt.Errorf("%s lets another account replace what is in it, so node_modules cannot be protected there; see https://docs.slopcop.com/checks/run-a-check/#local-review-on-windows", p)
		}
		if othersMayPlant(true, grants) || othersMayAddFolders(grants) {
			if err = lockNodeModules(filepath.Join(p, "node_modules")); err != nil {
				return err
			}
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

// namedAccess reads the owner and access list of path.
func namedAccess(path string) (bool, []aclGrant, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, nil, fmt.Errorf("read the access list of %s: %w", path, err)
	}
	ownerTrusted, grants, err := readAccess(sd)
	if err != nil {
		return false, nil, fmt.Errorf("read the access list of %s: %w", path, err)
	}
	return ownerTrusted, grants, nil
}

// readAccess reports whether the owner is trusted and lists the entries that
// apply to the object itself.
func readAccess(sd *windows.SECURITY_DESCRIPTOR) (bool, []aclGrant, error) {
	trusted, err := trustedSIDs()
	if err != nil {
		return false, nil, err
	}
	isTrusted := func(sid *windows.SID) bool {
		return slices.ContainsFunc(trusted, func(t *windows.SID) bool { return t.Equals(sid) })
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return false, nil, err
	}
	dacl, _, err := sd.DACL()
	// An object with no access list grants everyone full control.
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || (err == nil && dacl == nil) {
		return isTrusted(owner), []aclGrant{{mask: accessGenericAll}}, nil
	}
	if err != nil {
		return false, nil, err
	}
	var grants []aclGrant
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(dacl, i, &ace); err != nil {
			return false, nil, err
		}
		grant := aclGrant{
			inheritOnly: ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0,
			inheritable: ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) != 0,
		}
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			grant.trusted = isTrusted((*windows.SID)(unsafe.Pointer(&ace.SidStart)))
			grant.mask = uint32(ace.Mask)
			grants = append(grants, grant)
		case windows.ACCESS_DENIED_ACE_TYPE, accessDeniedObjectACEType, accessDeniedCallbackACEType, accessDeniedCallbackObjectACEType:
			// Ignoring a deny can only overstate who has access.
		default:
			// An allow entry of another kind may grant anyone anything.
			grant.mask = accessGenericAll
			grants = append(grants, grant)
		}
	}
	return isTrusted(owner), grants, nil
}

// trustedSIDs lists the accounts that may write above an adapter's directory.
func trustedSIDs() ([]*windows.SID, error) {
	me, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read the current user: %w", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	installer, err := windows.StringToSid(trustedInstallerSID)
	if err != nil {
		return nil, err
	}
	// An inherited CREATOR OWNER entry becomes the account creating the child.
	creator, err := windows.CreateWellKnownSid(windows.WinCreatorOwnerSid)
	if err != nil {
		return nil, err
	}
	return []*windows.SID{me.User.Sid, system, admins, installer, creator}, nil
}
