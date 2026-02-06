package commerce

import "time"

// Product represents a market-specific product with local pricing and
// pre-assigned currency-neutral CV points.
type Product struct {
	ID                string
	Name              string
	MarketID          string
	CategoryID        string
	ProductClassID    string // Drives volume routing rules in Network Engine
	RetailPrice       float64
	DistributorPrice  float64
	Currency          string  // Market's local currency
	CVPoints          float64 // Currency-neutral, pre-assigned
	QuantityAvailable int
	IsActive          bool
}

// ProductFilter supports filtering products.
type ProductFilter struct {
	MarketID       string // Required for most queries
	CategoryID     string // Optional
	ProductClassID string // Optional
	ActiveOnly     bool
	Page           int
	PageSize       int
}

// ProductPage is a paginated result of products.
type ProductPage struct {
	Products   []Product
	TotalCount int
	Page       int
	PageSize   int
}

// Category represents a product category.
type Category struct {
	ID   string
	Name string
}

// AutoshipConfig represents a user's autoship subscription.
type AutoshipConfig struct {
	ID                string
	UserID            string
	Items             []AutoshipItem
	Frequency         string // "monthly", "biweekly", etc.
	NextRunDate       time.Time
	Status            string // "active", "paused", "cancelled"
	ShippingAddressID string // References Identity
	PaymentMethodID   string // References Financial
	ResumeDate        time.Time
}

// AutoshipItem is a single product in an autoship subscription.
type AutoshipItem struct {
	ProductID string
	Quantity  int
}
