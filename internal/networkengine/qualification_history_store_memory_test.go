package networkengine

import "testing"

func TestMemoryQualificationHistoryStore_ImplementsInterface(t *testing.T) {
	var _ QualificationHistoryStore = (*MemoryQualificationHistoryStore)(nil)
}
