package networkengine

// SignupProduct represents a specific regional instance of a signup product.
// The same logical product (e.g., "Gold Package") has different regional
// instances with different pricing in local currency but comparable CV values.
type SignupProduct struct {
	ID                  string
	Name                string
	MarketID            string  // Regional market ("US", "EU", "JP")
	SignupFee           float64 // In market's local currency
	SignupFeeCurrency   string
	RecurringFee        float64         // In market's local currency
	RecurringPeriod     string          // "monthly", "annual"
	QualifiedStructures []string        // Necessary but not sufficient — placement requirements add gates
	CommissionEligible  map[string]bool // Keyed by structure ID
	AutoshipRequired    bool
	UpgradeEligible     []string // Which signup products this can be upgraded to
}

// StructureDescriptor describes a tree structure's shape and behavior.
type StructureDescriptor struct {
	ID                 string
	Type               string // "unilevel", "binary", "matrix", "stairstep", "streamline"
	Name               string
	MaxDepth           int // For matrix
	Width              int // For matrix (binary is always 2)
	CompressionEnabled bool
}

// VolumeRoutingRule maps a product class to a structure for volume attribution.
type VolumeRoutingRule struct {
	ProductClassID string
	StructureID    string
	Multiplier     float64 // Optional — different CV ratios per structure
}
