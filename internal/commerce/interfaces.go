package commerce

import (
	"context"
	"time"
)

// ProductCatalog provides read-only access to product data.
// Market-aware — products have regional pricing in local currency
// and currency-neutral CV points pre-assigned at configuration time.
// No cross-currency conversion — payment gateway handles that.
type ProductCatalog interface {
	GetProduct(ctx context.Context, productID string) (Product, error)
	ListProducts(ctx context.Context, filter ProductFilter) (ProductPage, error)
	GetCategories(ctx context.Context) ([]Category, error)
	GetProductsByCategory(ctx context.Context, categoryID string, filter ProductFilter) (ProductPage, error)
}

// AutoshipManager handles autoship subscription configuration.
// References shipping address (Identity) and payment method (Financial)
// by ID — Portals resolves them for display.
type AutoshipManager interface {
	GetForUser(ctx context.Context, userID string) (AutoshipConfig, error)
	UpdateItems(ctx context.Context, userID string, items []AutoshipItem) error
	UpdateSchedule(ctx context.Context, userID string, frequency string, nextRunDate time.Time) error
	Pause(ctx context.Context, userID string, resumeDate time.Time) error
	Cancel(ctx context.Context, userID string, reason string) error
}
