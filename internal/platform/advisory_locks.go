package platform

// Use one of these as the first key of a two-key advisory lock. Changing a value
// stops old and new binaries excluding each other.
const (
	// TreeLockNamespace spells "tree" in ASCII.
	TreeLockNamespace int32 = 0x74726565
	// MigrateLockNamespace spells "migr" in ASCII.
	MigrateLockNamespace int32 = 0x6d696772
)

// advisoryLockNamespaces lists the namespace constants above.
var advisoryLockNamespaces = []int32{TreeLockNamespace, MigrateLockNamespace}
