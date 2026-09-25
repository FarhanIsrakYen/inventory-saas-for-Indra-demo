package app

import (
	"fmt"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type API struct {
	db             *gorm.DB
	secret         []byte
	log            *slog.Logger
	elasticEnabled bool
	elasticURL     string
	elasticClient  *http.Client
	courier        *CourierService
	orderService   *OrderService
}
type Claims struct {
	UserID   string `json:"userId"`
	TenantID string `json:"tenantId"`
	jwt.RegisteredClaims
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func envBool(k string, d bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(k)))
	if v == "" {
		return d
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
func Run() {
	dsn := env("DATABASE_URL", "postgres://stockpilot:stockpilot_dev@localhost:5432/stockpilot?sslmode=disable")
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)})
	if err != nil {
		panic(err)
	}
	if err = db.AutoMigrate(&Tenant{}, &User{}, &Role{}, &TenantUser{}, &RefreshToken{}, &Product{}, &Variant{}, &ProductImage{}, &InventoryTransaction{}, &DeliveryOption{}, &Order{}, &OrderItem{}, &Shipment{}, &AuditLog{}); err != nil {
		panic(err)
	}
	api := &API{db: db, secret: []byte(env("JWT_SECRET", "development-secret-change-me-32-chars")), log: slog.Default(), elasticEnabled: envBool("ELASTICSEARCH_ENABLED", false), elasticURL: strings.TrimRight(env("ELASTICSEARCH_URL", "http://localhost:9200"), "/"), elasticClient: &http.Client{Timeout: 5 * time.Second}}
	pathaoStoreID, _ := strconv.Atoi(env("PATHAO_STORE_ID", "0"))
	pathao := NewPathaoProvider(PathaoConfig{BaseURL: env("PATHAO_BASE_URL", "https://courier-api-sandbox.pathao.com"), ClientID: os.Getenv("PATHAO_CLIENT_ID"), ClientSecret: os.Getenv("PATHAO_CLIENT_SECRET"), Username: os.Getenv("PATHAO_USERNAME"), Password: os.Getenv("PATHAO_PASSWORD"), StoreID: pathaoStoreID}, &http.Client{Timeout: 20 * time.Second})
	api.courier = NewCourierService(pathao)
	api.orderService = NewOrderService(db, api.courier)
	if api.elasticEnabled {
		go api.rebuildProductIndex()
	}
	r := api.routes()
	r.Run(":" + env("PORT", "8080"))
}
func (a *API) routes() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), requestLog(a.log), cors.New(cors.Config{AllowOrigins: []string{"http://localhost:5173"}, AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}, AllowHeaders: []string{"Authorization", "Content-Type"}, AllowCredentials: true}))
	r.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })
	api := r.Group("/api")
	auth := api.Group("/auth")
	auth.POST("/register", a.register)
	auth.POST("/login", a.login)
	auth.POST("/refresh", a.refresh)
	auth.POST("/logout", a.logout)
	p := api.Group("", a.auth)
	p.GET("/auth/me", a.me)
	p.GET("/tenants", a.tenants)
	p.POST("/tenants/switch", a.switchTenant)
	p.GET("/dashboard", a.dashboard)
	p.GET("/products", a.listProducts)
	p.POST("/products", a.require("products.create"), a.createProduct)
	p.POST("/products/import/1688", a.require("products.create"), a.import1688)
	p.GET("/products/:id", a.product)
	p.PUT("/products/:id", a.require("products.update"), a.updateProduct)
	p.DELETE("/products/:id", a.require("products.delete"), a.deleteProduct)
	p.POST("/inventory/adjustments", a.require("inventory.adjust"), a.adjustStock)
	p.GET("/inventory/transactions", a.listTransactions)
	p.GET("/orders", a.listOrders)
	p.POST("/orders", a.require("orders.create"), a.createOrder)
	p.GET("/orders/:id", a.order)
	p.GET("/orders/:id/courier/pathao", a.getPathaoOrder)
	p.POST("/orders/:id/courier/pathao", a.require("orders.update"), a.createPathaoOrder)
	p.PUT("/orders/:id", a.require("orders.update"), a.updateOrder)
	p.PUT("/orders/:id/status", a.require("orders.update"), a.updateOrderStatus)
	p.DELETE("/orders/:id", a.require("orders.cancel"), a.deleteOrder)
	p.GET("/delivery-options", a.listDelivery)
	p.POST("/delivery-options", a.require("settings.update"), a.createDelivery)
	p.PUT("/delivery-options/:id", a.require("settings.update"), a.updateDelivery)
	p.DELETE("/delivery-options/:id", a.require("settings.update"), a.deleteDelivery)
	p.GET("/audit-logs", a.require("settings.read"), a.auditLogs)
	return r
}
func requestLog(l *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		id := uuid.NewString()
		c.Header("X-Request-ID", id)
		c.Next()
		l.Info("request", "request_id", id, "method", c.Request.Method, "path", c.Request.URL.Path, "status", c.Writer.Status(), "duration_ms", time.Since(start).Milliseconds())
	}
}
func fail(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"success": false, "code": code, "message": msg})
}
func ok(c *gin.Context, data any) { c.JSON(http.StatusOK, gin.H{"success": true, "data": data}) }
func (a *API) token(uid, tid uuid.UUID, ttl time.Duration) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{UserID: uid.String(), TenantID: tid.String(), RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)), IssuedAt: jwt.NewNumericDate(time.Now())}}).SignedString(a.secret)
}
func (a *API) auth(c *gin.Context) {
	raw := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	claims := &Claims{}
	t, e := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) { return a.secret, nil })
	if e != nil || !t.Valid {
		fail(c, 401, "UNAUTHENTICATED", "A valid session is required")
		c.Abort()
		return
	}
	uid, e1 := uuid.Parse(claims.UserID)
	tid, e2 := uuid.Parse(claims.TenantID)
	if e1 != nil || e2 != nil {
		fail(c, 401, "UNAUTHENTICATED", "Invalid session")
		c.Abort()
		return
	}
	var m TenantUser
	if a.db.Where("tenant_id = ? AND user_id = ? AND status = ?", tid, uid, "active").First(&m).Error != nil {
		fail(c, 403, "TENANT_ACCESS_DENIED", "Workspace access denied")
		c.Abort()
		return
	}
	c.Set("uid", uid)
	c.Set("tid", tid)
	c.Set("membership", m)
	c.Next()
}
func (a *API) require(permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		m := c.MustGet("membership").(TenantUser)
		var role Role
		if a.db.First(&role, m.RoleID).Error != nil || !(role.Name == "Owner" || strings.Contains(role.Permissions, permission)) {
			fail(c, 403, "PERMISSION_DENIED", "You don't have permission for this action")
			c.Abort()
			return
		}
		c.Next()
	}
}
func ids(c *gin.Context) (uuid.UUID, uuid.UUID) {
	return c.MustGet("uid").(uuid.UUID), c.MustGet("tid").(uuid.UUID)
}
func (a *API) audit(t, u uuid.UUID, action, typ, id string) {
	a.db.Create(&AuditLog{Base: Base{ID: uuid.New()}, TenantID: t, UserID: u, Action: action, EntityType: typ, EntityID: id})
}
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	return fmt.Sprintf("%s-%s", s[:min(len(s), 36)], uuid.NewString()[:6])
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func hash(p string) (string, error) {
	v, e := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(v), e
}
