package review

import "testing"

// TestMacACLGrantsWrite checks listings in the format macOS `ls -lde` prints.
func TestMacACLGrantsWrite(t *testing.T) {
	const head = "drwx------+ 239 me  staff  7648 Nov 23 15:52 /Users/me/Library/Caches\n"
	for _, c := range []struct {
		name, entries string
		want          bool
	}{
		{"no entries", "", false},
		{"default home", " 0: group:everyone deny delete\n", false},
		{"read only", " 0: user:nobody allow list,search,readattr\n", false},
		{"add files", " 0: user:nobody allow add_file\n", true},
		{"add folders", " 0: group:staff allow list,add_subdirectory\n", true},
		{"inherited", " 0: user:nobody inherited allow add_file\n", true},
		{"name with a space", " 0: group:Domain Users allow delete_child\n", true},
		{"change the access list", " 0: user:nobody allow writesecurity\n", true},
		{"take ownership", " 0: user:nobody allow chown\n", true},
		{"after a deny", " 0: group:everyone deny delete\n 1: user:nobody allow add_file\n", true},
		{"children only", " 0: user:nobody allow add_file,file_inherit,directory_inherit,only_inherit\n", false},
		{"denied write", " 0: user:nobody deny add_file\n", false},
	} {
		if got := macACLGrantsWrite(head + c.entries); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// TestMacACLPassesWrite checks entries that reach only what a directory holds.
func TestMacACLPassesWrite(t *testing.T) {
	for entry, want := range map[string]bool{
		" 0: group:everyone deny delete\n":                            false,
		" 0: user:nobody allow add_file\n":                            false,
		" 0: user:nobody allow list,file_inherit,directory_inherit\n": false,
		" 0: user:nobody allow add_file,file_inherit,only_inherit\n":  true,
		" 0: user:nobody allow writesecurity,directory_inherit\n":     true,
		" 0: user:nobody allow add_subdirectory,directory_inherit\n":  true,
	} {
		if got := macACLPassesWrite("drwx------+ 2 me staff 64 Nov 23 15:52 /c\n" + entry); got != want {
			t.Errorf("%q: got %v, want %v", entry, got, want)
		}
	}
}

// TestWindowsGrantsForChildrenAndReplacement checks entries that reach what a
// directory holds, and rights that let someone remove a locked folder.
func TestWindowsGrantsForChildrenAndReplacement(t *testing.T) {
	childOnly := aclGrant{inheritOnly: true, inheritable: true, mask: accessWriteDAC}
	if othersMayPlant(true, []aclGrant{childOnly}) || othersMayAddFolders([]aclGrant{childOnly}) || othersMayReplaceEntries(true, []aclGrant{childOnly}) {
		t.Error("an inherit-only entry counted for the directory itself")
	}
	if !othersInherit([]aclGrant{childOnly}) {
		t.Error("an inherit-only write entry was not passed on")
	}
	if othersInherit([]aclGrant{{trusted: true, inheritable: true, mask: accessGenericAll}, {inheritable: true, mask: 0x001200A9}, {mask: accessAddFile}}) {
		t.Error("counted a trusted, read-only or non-inheritable entry")
	}
	for _, mask := range []uint32{accessDeleteChild, accessWriteDAC, accessWriteOwner, accessGenericAll} {
		if !othersMayReplaceEntries(true, []aclGrant{{mask: mask}}) {
			t.Errorf("mask %#x can replace entries", mask)
		}
	}
	// The default Modify grant on a secondary drive cannot remove a child
	// whose own list refuses it.
	if othersMayReplaceEntries(true, []aclGrant{{mask: accessAddFile | accessAddSubdirectory | accessDelete}}) {
		t.Error("Modify counted as able to replace entries")
	}
	if !othersMayReplaceEntries(false, nil) {
		t.Error("an untrusted owner can replace entries")
	}
}

// TestWindowsGrants checks the access-list decisions, including the default
// root of a Windows system drive, where any signed-in user can add a folder.
func TestWindowsGrants(t *testing.T) {
	const read = 0x001200A9
	driveRoot := []aclGrant{
		{trusted: true, mask: accessGenericAll},
		{mask: read},
		{mask: accessAddSubdirectory},
	}
	for _, c := range []struct {
		name         string
		ownerTrusted bool
		grants       []aclGrant
		plant, add   bool
	}{
		{"private", true, []aclGrant{{trusted: true, mask: accessGenericAll}}, false, false},
		{"others may read", true, []aclGrant{{mask: read}}, false, false},
		{"owned by another user", false, []aclGrant{{trusted: true, mask: accessGenericAll}}, true, false},
		{"others may add files", true, []aclGrant{{mask: accessAddFile}}, true, false},
		{"others may replace entries", true, []aclGrant{{mask: accessDeleteChild}}, true, false},
		{"others may change the access list", true, []aclGrant{{mask: accessWriteDAC}}, true, false},
		{"others may take ownership", true, []aclGrant{{mask: accessWriteOwner}}, true, false},
		{"others have generic write", true, []aclGrant{{mask: accessGenericWrite}}, true, false},
		{"others have full control", true, []aclGrant{{mask: accessGenericAll}}, true, false},
		{"default drive root", true, driveRoot, false, true},
		{"only trusted may add folders", true, []aclGrant{{trusted: true, mask: accessAddSubdirectory}}, false, false},
	} {
		if got := othersMayPlant(c.ownerTrusted, c.grants); got != c.plant {
			t.Errorf("%s: othersMayPlant = %v, want %v", c.name, got, c.plant)
		}
		if got := othersMayAddFolders(c.grants); got != c.add {
			t.Errorf("%s: othersMayAddFolders = %v, want %v", c.name, got, c.add)
		}
	}
}
