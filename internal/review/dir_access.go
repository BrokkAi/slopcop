package review

import (
	"regexp"
	"slices"
	"strings"
)

// macACLAllow matches an allow entry in the output of macOS `ls -le`.
var macACLAllow = regexp.MustCompile(`^\s*\d+: .* allow ([a-z_,]+)\s*$`)

// macWriteRights are the macOS ACL rights that let their holder plant or
// replace something in a directory, or change who may.
var macWriteRights = []string{"add_file", "add_subdirectory", "delete_child", "delete", "write", "append", "writesecurity", "chown"}

// macACLGrantsWrite reports whether a directory's `ls -lde` listing has an
// entry giving anyone write access to the directory itself. Permission bits
// do not show these grants. An entry for the user's own account counts too,
// since macOS default directories have none.
func macACLGrantsWrite(listing string) bool {
	return macACLHasWrite(listing, func(rights []string) bool {
		return !slices.Contains(rights, "only_inherit")
	})
}

// macACLPassesWrite reports whether a directory's listing has an entry that
// gives write access to what is created in the directory.
func macACLPassesWrite(listing string) bool {
	return macACLHasWrite(listing, func(rights []string) bool {
		return slices.Contains(rights, "file_inherit") || slices.Contains(rights, "directory_inherit")
	})
}

// macACLHasWrite reports whether an allow entry selected by applies has a
// write right.
func macACLHasWrite(listing string, applies func([]string) bool) bool {
	for _, line := range strings.Split(listing, "\n") {
		m := macACLAllow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rights := strings.Split(m[1], ",")
		if applies(rights) && slices.ContainsFunc(rights, func(r string) bool { return slices.Contains(macWriteRights, r) }) {
			return true
		}
	}
	return false
}

// Windows access rights on a directory.
const (
	accessAddFile         = 0x00000002
	accessAddSubdirectory = 0x00000004
	accessDeleteChild     = 0x00000040
	accessDelete          = 0x00010000
	accessWriteDAC        = 0x00040000
	accessWriteOwner      = 0x00080000
	accessGenericAll      = 0x10000000
	accessGenericWrite    = 0x40000000

	// accessPlantOrTakeOver lets its holder add files to a directory, replace
	// what is in it, or change who may do so.
	accessPlantOrTakeOver = accessAddFile | accessDeleteChild | accessDelete | accessWriteDAC | accessWriteOwner | accessGenericAll | accessGenericWrite
)

// aclGrant is one allow entry of a directory's access list. trusted means the
// current user, SYSTEM, Administrators, TrustedInstaller or CREATOR OWNER.
// inheritOnly entries apply only to what is created in the directory, and
// inheritable entries are passed on to it.
type aclGrant struct {
	trusted, inheritOnly, inheritable bool
	mask                              uint32
}

// othersMayPlant reports whether a directory has an untrusted owner, or grants
// someone untrusted the right to add files, replace entries or change access.
func othersMayPlant(ownerTrusted bool, grants []aclGrant) bool {
	return !ownerTrusted || slices.ContainsFunc(grants, func(g aclGrant) bool {
		return !g.trusted && !g.inheritOnly && g.mask&accessPlantOrTakeOver != 0
	})
}

// othersMayReplaceEntries reports whether someone untrusted can remove an
// entry of a directory whatever that entry's own access list says.
func othersMayReplaceEntries(ownerTrusted bool, grants []aclGrant) bool {
	return !ownerTrusted || slices.ContainsFunc(grants, func(g aclGrant) bool {
		return !g.trusted && !g.inheritOnly && g.mask&(accessDeleteChild|accessWriteDAC|accessWriteOwner|accessGenericAll) != 0
	})
}

// othersInherit reports whether a directory passes write access for someone
// untrusted to what is created in it.
func othersInherit(grants []aclGrant) bool {
	return slices.ContainsFunc(grants, func(g aclGrant) bool {
		return !g.trusted && g.inheritable && g.mask&(accessPlantOrTakeOver|accessAddSubdirectory) != 0
	})
}

// othersMayAddFolders reports whether someone untrusted can create folders in
// a directory. Windows grants this to every signed-in user in the root of the
// system drive, where a planted node_modules is on npm's PATH and Node's
// module search for everything on that drive.
func othersMayAddFolders(grants []aclGrant) bool {
	return slices.ContainsFunc(grants, func(g aclGrant) bool {
		return !g.trusted && !g.inheritOnly && g.mask&accessAddSubdirectory != 0
	})
}
