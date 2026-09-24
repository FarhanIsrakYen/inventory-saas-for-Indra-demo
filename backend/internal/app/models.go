package app

import (
	"github.com/google/uuid"
	"time"
)

type Base struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type Tenant struct {
	Base
	Name              string `json:"name"`
	Slug              string `gorm:"uniqueIndex" json:"slug"`
	Status            string `json:"status"`
	LowStockThreshold int    `json:"lowStockThreshold"`
	Currency          string `json:"currency"`
}
type User struct {
	Base
	Email        string       `gorm:"uniqueIndex" json:"email"`
	PasswordHash string       `json:"-"`
	Name         string       `json:"name"`
	Status       string       `json:"status"`
	Memberships  []TenantUser `json:"-"`
}
type Role struct {
	Base
	TenantID    uuid.UUID `gorm:"type:uuid;index"`
	Name        string    `json:"name"`
	Permissions string    `json:"permissions"`
}
type TenantUser struct {
	Base
	TenantID uuid.UUID `gorm:"type:uuid;uniqueIndex:idx_tenant_user"`
	UserID   uuid.UUID `gorm:"type:uuid;uniqueIndex:idx_tenant_user"`
	RoleID   uuid.UUID `gorm:"type:uuid"`
	Role     Role      `json:"role"`
	User     User      `json:"user"`
	Status   string    `json:"status"`
}
type RefreshToken struct {
	Base
	UserID    uuid.UUID `gorm:"type:uuid;index"`
	TenantID  uuid.UUID `gorm:"type:uuid;index"`
	TokenHash string    `json:"-"`
	ExpiresAt time.Time
}
type Product struct {
	Base
	TenantID     uuid.UUID      `gorm:"type:uuid;uniqueIndex:idx_product_sku;index" json:"-"`
	Name         string         `json:"name"`
	SKU          string         `gorm:"uniqueIndex:idx_product_sku" json:"sku"`
	Description  string         `json:"description"`
	Currency     string         `json:"currency"`
	Price        int64          `json:"price"`
	FacebookURL  string         `json:"facebookUrl"`
	InstagramURL string         `json:"instagramUrl"`
	Variants     []Variant      `json:"variants"`
	Images       []ProductImage `gorm:"polymorphic:Owner" json:"images"`
}
type Variant struct {
	Base
	TenantID  uuid.UUID      `gorm:"type:uuid;index" json:"-"`
	ProductID uuid.UUID      `gorm:"type:uuid;index" json:"productId"`
	SKU       string         `json:"sku"`
	Color     string         `json:"color"`
	Size      string         `json:"size"`
	Quantity  int            `gorm:"index" json:"quantity"`
	Price     int64          `json:"price"`
	Images    []ProductImage `gorm:"polymorphic:Owner" json:"images"`
}
type ProductImage struct {
	Base
	TenantID  uuid.UUID `gorm:"type:uuid;index" json:"-"`
	OwnerID   uuid.UUID `gorm:"type:uuid;index" json:"ownerId"`
	OwnerType string    `json:"ownerType"`
	URL       string    `json:"url"`
	Alt       string    `json:"alt"`
	Scope     string    `json:"scope"`
}
type InventoryTransaction struct {
	Base
	TenantID         uuid.UUID `gorm:"type:uuid;index" json:"-"`
	VariantID        uuid.UUID `gorm:"type:uuid;index" json:"variantId"`
	UserID           uuid.UUID `gorm:"type:uuid" json:"userId"`
	PreviousQuantity int       `json:"previousQuantity"`
	Change           int       `json:"change"`
	NewQuantity      int       `json:"newQuantity"`
	Reason           string    `json:"reason"`
}
type DeliveryOption struct {
	Base
	TenantID uuid.UUID `gorm:"type:uuid;index" json:"-"`
	Name     string    `json:"name"`
	Price    int64     `json:"price"`
	Currency string    `json:"currency"`
	Enabled  bool      `json:"enabled"`
}
type Order struct {
	Base
	TenantID         uuid.UUID   `gorm:"type:uuid;index" json:"-"`
	Number           string      `gorm:"uniqueIndex" json:"number"`
	CustomerName     string      `json:"customerName"`
	CustomerPhone    string      `gorm:"index" json:"customerPhone"`
	CustomerAddress  string      `json:"customerAddress"`
	Notes            string      `json:"notes"`
	Status           string      `gorm:"index" json:"status"`
	Currency         string      `json:"currency"`
	DeliveryOptionID *uuid.UUID  `gorm:"type:uuid" json:"deliveryOptionId"`
	DeliveryName     string      `json:"deliveryName"`
	DeliveryCharge   int64       `json:"deliveryCharge"`
	Subtotal         int64       `json:"subtotal"`
	Total            int64       `json:"total"`
	Items            []OrderItem `json:"items"`
}
type OrderItem struct {
	Base
	TenantID    uuid.UUID `gorm:"type:uuid;index" json:"-"`
	OrderID     uuid.UUID `gorm:"type:uuid;index" json:"orderId"`
	VariantID   uuid.UUID `gorm:"type:uuid" json:"variantId"`
	ProductName string    `json:"productName"`
	VariantName string    `json:"variantName"`
	ProductImageURL string `json:"productImageUrl"`
	Quantity    int       `json:"quantity"`
	UnitPrice   int64     `json:"unitPrice"`
	Subtotal    int64     `json:"subtotal"`
}
type AuditLog struct {
	Base
	TenantID   uuid.UUID `gorm:"type:uuid;index" json:"-"`
	UserID     uuid.UUID `gorm:"type:uuid;index" json:"userId"`
	Action     string    `json:"action"`
	EntityType string    `json:"entityType"`
	EntityID   string    `json:"entityId"`
	Metadata   string    `json:"metadata"`
}
