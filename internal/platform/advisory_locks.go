package platform

// Every advisory lock mlmforge takes uses the two-key form, with one of these as
// the first key. Changing a value stops old and new binaries excluding each other.
const (
	// TreeLockNamespace spells "tree" in ASCII.
	TreeLockNamespace int32 = 0x74726565
	// MigrateLockNamespace spells "migr" in ASCII.
	MigrateLockNamespace int32 = 0x6d696772
)

// advisoryLockNamespaces lists every advisory lock namespace.
var advisoryLockNamespaces = []int32{TreeLockNamespace, MigrateLockNamespace}
