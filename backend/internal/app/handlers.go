package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type registerInput struct {
	Name         string `json:"name" binding:"required,min=2"`
	Email        string `json:"email" binding:"required,email"`
	Password     string `json:"password" binding:"required,min=8"`
	BusinessName string `json:"businessName" binding:"required,min=2"`
}

func (a *API) register(c *gin.Context) {
	var in registerInput
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "VALIDATION_ERROR", "Please provide a valid name, email, password and business name")
		return
	}
	tx := a.db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()
	p, _ := hash(in.Password)
	u := User{Base: Base{ID: uuid.New()}, Name: in.Name, Email: strings.ToLower(in.Email), PasswordHash: p, Status: "active"}
	if e := tx.Create(&u).Error; e != nil {
		tx.Rollback()
		fail(c, 409, "EMAIL_IN_USE", "That email is already registered")
		return
	}
	t := Tenant{Base: Base{ID: uuid.New()}, Name: in.BusinessName, Slug: slug(in.BusinessName), Status: "active", LowStockThreshold: 5, Currency: "BDT"}
	tx.Create(&t)
	r := Role{Base: Base{ID: uuid.New()}, TenantID: t.ID, Name: "Owner", Permissions: "*"}
	tx.Create(&r)
	tx.Create(&TenantUser{Base: Base{ID: uuid.New()}, TenantID: t.ID, UserID: u.ID, RoleID: r.ID, Status: "active"})
	for _, x := range []DeliveryOption{{Name: "Inside Dhaka", Price: 80}, {Name: "Outside Dhaka", Price: 120}} {
		x.Base = Base{ID: uuid.New()}
		x.TenantID = t.ID
		x.Currency = "BDT"
		x.Enabled = true
		tx.Create(&x)
	}
	if e := tx.Commit().Error; e != nil {
		fail(c, 500, "REGISTRATION_FAILED", "Unable to create workspace")
		return
	}
	a.issue(c, u, t.ID)
}

type loginInput struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

func (a *API) login(c *gin.Context) {
	var in loginInput
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "VALIDATION_ERROR", "Enter your email and password")
		return
	}
	var u User
	if a.db.Where("email = ?", strings.ToLower(in.Email)).First(&u).Error != nil || bcryptCompare(u.PasswordHash, in.Password) != nil {
		fail(c, 401, "INVALID_CREDENTIALS", "Email or password is incorrect")
		return
	}
	var m TenantUser
	if a.db.Where("user_id=? AND status=?", u.ID, "active").First(&m).Error != nil {
		fail(c, 403, "NO_WORKSPACE", "No active workspace is available")
		return
	}
	a.issue(c, u, m.TenantID)
}
func bcryptCompare(h, p string) error { return bcrypt.CompareHashAndPassword([]byte(h), []byte(p)) }
func (a *API) issue(c *gin.Context, u User, tid uuid.UUID) {
	access, e := a.token(u.ID, tid, 15*time.Minute)
	if e != nil {
		fail(c, 500, "TOKEN_ERROR", "Unable to create a session")
		return
	}
	refresh, e := a.token(u.ID, tid, 30*24*time.Hour)
	if e != nil {
		fail(c, 500, "TOKEN_ERROR", "Unable to create a session")
		return
	}
	sum := sha256.Sum256([]byte(refresh))
	a.db.Create(&RefreshToken{Base: Base{ID: uuid.New()}, UserID: u.ID, TenantID: tid, TokenHash: fmt.Sprintf("%x", sum), ExpiresAt: time.Now().Add(30 * 24 * time.Hour)})
	var t Tenant
	a.db.First(&t, tid)
	ok(c, gin.H{"accessToken": access, "refreshToken": refresh, "user": u, "tenant": t})
}
func (a *API) refresh(c *gin.Context) {
	var in struct {
		RefreshToken string `json:"refreshToken" binding:"required"`
	}
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "VALIDATION_ERROR", "Refresh token required")
		return
	}
	cl := &Claims{}
	tok, e := jwt.ParseWithClaims(in.RefreshToken, cl, func(t *jwt.Token) (any, error) { return a.secret, nil })
	if e != nil || !tok.Valid {
		fail(c, 401, "INVALID_REFRESH_TOKEN", "Session has expired")
		return
	}
	sum := sha256.Sum256([]byte(in.RefreshToken))
	var rt RefreshToken
	if a.db.Where("token_hash=? AND expires_at > ?", fmt.Sprintf("%x", sum), time.Now()).First(&rt).Error != nil {
		fail(c, 401, "INVALID_REFRESH_TOKEN", "Session has expired")
		return
	}
	a.db.Delete(&rt)
	var u User
	a.db.First(&u, rt.UserID)
	a.issue(c, u, rt.TenantID)
}
func (a *API) logout(c *gin.Context) {
	var in struct {
		RefreshToken string `json:"refreshToken"`
	}
	c.ShouldBindJSON(&in)
	sum := sha256.Sum256([]byte(in.RefreshToken))
	a.db.Where("token_hash=?", fmt.Sprintf("%x", sum)).Delete(&RefreshToken{})
	ok(c, gin.H{"message": "Logged out"})
}
func (a *API) me(c *gin.Context) {
	u, t := ids(c)
	var user User
	var tenant Tenant
	a.db.First(&user, u)
	a.db.First(&tenant, t)
	ok(c, gin.H{"user": user, "tenant": tenant})
}
func (a *API) tenants(c *gin.Context) {
	u, _ := ids(c)
	var m []TenantUser
	a.db.Preload("Role").Preload("User").Where("user_id=? AND status=?", u, "active").Find(&m)
	out := []Tenant{}
	for _, x := range m {
		var t Tenant
		if a.db.First(&t, x.TenantID).Error == nil {
			out = append(out, t)
		}
	}
	ok(c, out)
}
func (a *API) switchTenant(c *gin.Context) {
	u, _ := ids(c)
	var in struct {
		TenantID uuid.UUID `json:"tenantId" binding:"required"`
	}
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "VALIDATION_ERROR", "Workspace required")
		return
	}
	var m TenantUser
	if a.db.Where("tenant_id=? AND user_id=? AND status=?", in.TenantID, u, "active").First(&m).Error != nil {
		fail(c, 404, "WORKSPACE_NOT_FOUND", "Workspace not found")
		return
	}
	var user User
	a.db.First(&user, u)
	a.issue(c, user, in.TenantID)
}

type imageInput string

func (i *imageInput) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		*i = imageInput(s)
		return nil
	}
	var obj struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	*i = imageInput(obj.URL)
	return nil
}

type variantInput struct {
	ID       uuid.UUID    `json:"id"`
	SKU      string       `json:"sku"`
	Color    string       `json:"color"`
	Size     string       `json:"size"`
	Quantity int          `json:"quantity" binding:"gte=0"`
	Price    int64        `json:"price" binding:"gte=0"`
	Images   []imageInput `json:"images"`
}
type productInput struct {
	Name         string         `json:"name" binding:"required"`
	SKU          string         `json:"sku" binding:"required"`
	Description  string         `json:"description"`
	Currency     string         `json:"currency"`
	Price        int64          `json:"price" binding:"gte=0"`
	FacebookURL  string         `json:"facebookUrl"`
	InstagramURL string         `json:"instagramUrl"`
	Variants     []variantInput `json:"variants"`
	Images       []string       `json:"images"`
}

func (a *API) listProducts(c *gin.Context) {
	_, t := ids(c)
	q := a.db.Where("tenant_id=?", t).Preload("Variants.Images").Preload("Images")
	s := c.Query("search")
	if s != "" {
		q = q.Where("name ILIKE ? OR sku ILIKE ?", "%"+s+"%", "%"+s+"%")
	}
	if c.Query("stock") == "out" {
		q = q.Joins("JOIN product_variants ON product_variants.product_id = products.id").Where("product_variants.quantity = 0").Group("products.id")
	} else if c.Query("stock") == "low" {
		q = q.Joins("JOIN product_variants ON product_variants.product_id = products.id").Where("product_variants.quantity > 0 AND product_variants.quantity < 5").Group("products.id")
	}
	var items []Product
	var count int64
	q.Model(&Product{}).Count(&count)
	page, size := pageArgs(c)
	q.Order("created_at DESC").Offset((page - 1) * size).Limit(size).Find(&items)
	ok(c, gin.H{"items": items, "page": page, "size": size, "total": count})
}
func pageArgs(c *gin.Context) (int, int) {
	p, z := 1, 20
	fmt.Sscanf(c.DefaultQuery("page", "1"), "%d", &p)
	fmt.Sscanf(c.DefaultQuery("size", "20"), "%d", &z)
	if p < 1 {
		p = 1
	}
	if z < 1 || z > 100 {
		z = 20
	}
	return p, z
}
func (a *API) createProduct(c *gin.Context) {
	var in productInput
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "VALIDATION_ERROR", "Check the product fields and variant quantities")
		return
	}
	u, t := ids(c)
	tx := a.db.Begin()
	p := Product{Base: Base{ID: uuid.New()}, TenantID: t, Name: in.Name, SKU: in.SKU, Description: in.Description, Currency: currency(in.Currency), Price: in.Price, FacebookURL: in.FacebookURL, InstagramURL: in.InstagramURL}
	if e := tx.Create(&p).Error; e != nil {
		tx.Rollback()
		fail(c, 409, "SKU_EXISTS", "This SKU already exists in your workspace")
		return
	}
	for _, imageURL := range in.Images {
		tx.Create(&ProductImage{Base: Base{ID: uuid.New()}, TenantID: t, OwnerID: p.ID, OwnerType: "products", URL: imageURL, Scope: "product"})
	}
	for _, v := range in.Variants {
		variant := Variant{Base: Base{ID: uuid.New()}, TenantID: t, ProductID: p.ID, SKU: v.SKU, Color: v.Color, Size: v.Size, Quantity: v.Quantity, Price: choosePrice(v.Price, p.Price)}
		tx.Create(&variant)
		for _, imageURL := range v.Images {
			tx.Create(&ProductImage{Base: Base{ID: uuid.New()}, TenantID: t, OwnerID: variant.ID, OwnerType: "variants", URL: string(imageURL), Scope: "variant"})
		}
	}
	tx.Commit()
	a.audit(t, u, "product.created", "product", p.ID.String())
	a.productResponse(c, p.ID, t)
}
func choosePrice(v, d int64) int64 {
	if v > 0 {
		return v
	}
	return d
}
func firstImage(images []ProductImage) string {
	if len(images) > 0 {
		return images[0].URL
	}
	return ""
}
func currency(c string) string {
	switch c {
	case "USD", "EUR", "GBP", "BDT":
		return c
	}
	return "BDT"
}
func (a *API) productResponse(c *gin.Context, id, t uuid.UUID) {
	var p Product
	if a.db.Where("id=? AND tenant_id=?", id, t).Preload("Variants.Images").Preload("Images").First(&p).Error != nil {
		fail(c, 404, "PRODUCT_NOT_FOUND", "Product not found")
		return
	}
	ok(c, p)
}
func (a *API) product(c *gin.Context) {
	_, t := ids(c)
	id, e := uuid.Parse(c.Param("id"))
	if e != nil {
		fail(c, 404, "PRODUCT_NOT_FOUND", "Product not found")
		return
	}
	a.productResponse(c, id, t)
}
func (a *API) updateProduct(c *gin.Context) {
	u, t := ids(c)
	id, e := uuid.Parse(c.Param("id"))
	if e != nil {
		fail(c, 404, "PRODUCT_NOT_FOUND", "Product not found")
		return
	}
	var p Product
	if a.db.Where("id=? AND tenant_id=?", id, t).First(&p).Error != nil {
		fail(c, 404, "PRODUCT_NOT_FOUND", "Product not found")
		return
	}
	var in productInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, "VALIDATION_ERROR", "Invalid product payload: "+err.Error())
		return
	}
	p.Name = in.Name
	p.SKU = in.SKU
	p.Description = in.Description
	p.Price = in.Price
	p.Currency = currency(in.Currency)
	if e = a.db.Save(&p).Error; e != nil {
		fail(c, 409, "SKU_EXISTS", "This SKU already exists in your workspace")
		return
	}
	if in.Images != nil {
		a.db.Where("owner_id=? AND owner_type=? AND tenant_id=?", p.ID, "products", t).Delete(&ProductImage{})
		for _, imageURL := range in.Images {
			a.db.Create(&ProductImage{Base: Base{ID: uuid.New()}, TenantID: t, OwnerID: p.ID, OwnerType: "products", URL: imageURL, Scope: "product"})
		}
	}
	for _, input := range in.Variants {
		if input.ID == uuid.Nil {
			variant := Variant{Base: Base{ID: uuid.New()}, TenantID: t, ProductID: p.ID, SKU: input.SKU, Color: input.Color, Size: input.Size, Quantity: input.Quantity, Price: input.Price}
			a.db.Create(&variant)
			for _, imageURL := range input.Images {
				a.db.Create(&ProductImage{Base: Base{ID: uuid.New()}, TenantID: t, OwnerID: variant.ID, OwnerType: "variants", URL: string(imageURL), Scope: "variant"})
			}
			continue
		}
		updates := map[string]any{"sku": input.SKU, "color": input.Color, "size": input.Size, "quantity": input.Quantity, "price": input.Price}
		if a.db.Model(&Variant{}).Where("id=? AND product_id=? AND tenant_id=?", input.ID, p.ID, t).Updates(updates).RowsAffected == 0 {
			fail(c, 404, "VARIANT_NOT_FOUND", "One of the product variants was not found")
			return
		}
		if input.Images != nil {
			a.db.Where("owner_id=? AND owner_type=? AND tenant_id=?", input.ID, "variants", t).Delete(&ProductImage{})
			for _, imageURL := range input.Images {
				a.db.Create(&ProductImage{Base: Base{ID: uuid.New()}, TenantID: t, OwnerID: input.ID, OwnerType: "variants", URL: string(imageURL), Scope: "variant"})
			}
		}
	}
	a.audit(t, u, "product.updated", "product", id.String())
	a.productResponse(c, id, t)
}
func (a *API) deleteProduct(c *gin.Context) {
	u, t := ids(c)
	id, e := uuid.Parse(c.Param("id"))
	if e != nil || a.db.Where("id=? AND tenant_id=?", id, t).Delete(&Product{}).RowsAffected == 0 {
		fail(c, 404, "PRODUCT_NOT_FOUND", "Product not found")
		return
	}
	a.audit(t, u, "product.deleted", "product", id.String())
	c.Status(204)
}
func (a *API) import1688(c *gin.Context) {
	var in struct {
		URL string `json:"url" binding:"required,url"`
	}
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "VALIDATION_ERROR", "Enter a valid 1688 product URL")
		return
	}
	u, e := url.Parse(in.URL)
	if e != nil || !strings.HasSuffix(u.Host, "1688.com") {
		fail(c, 400, "UNSUPPORTED_IMPORT_URL", "Please provide a public 1688.com URL")
		return
	}
	client := &http.Client{Timeout: 12 * time.Second}
	request, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, in.URL, nil)
	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; StockPilotImporter/1.0)")
	response, err := client.Do(request)
	if err != nil || response.StatusCode < 200 || response.StatusCode > 299 {
		fail(c, 422, "IMPORT_UNAVAILABLE", "We couldn't import this public product page. Please enter the product details manually.")
		return
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		fail(c, 422, "IMPORT_UNAVAILABLE", "We couldn't read this public product page. Please enter the product details manually.")
		return
	}
	source := string(body)
	meta := func(property string) string {
		re := regexp.MustCompile(`(?is)<meta[^>]+(?:property|name)=["']` + regexp.QuoteMeta(property) + `["'][^>]+content=["']([^"']+)["']`)
		match := re.FindStringSubmatch(source)
		if len(match) == 2 {
			return html.UnescapeString(match[1])
		}
		return ""
	}
	title := meta("og:title")
	if title == "" {
		title = meta("title")
	}
	if title == "" {
		re := regexp.MustCompile(`(?is)<title[^>]*>\s*(.*?)\s*</title>`)
		if match := re.FindStringSubmatch(source); len(match) == 2 {
			title = html.UnescapeString(match[1])
		}
	}
	description := meta("og:description")
	if description == "" {
		description = meta("description")
	}
	images := []string{}
	if image := meta("og:image"); image != "" {
		images = append(images, image)
	}
	if title == "" && description == "" && len(images) == 0 {
		fail(c, 422, "IMPORT_UNAVAILABLE", "We couldn't find usable public product details. Please enter the product details manually.")
		return
	}
	ok(c, gin.H{"name": title, "description": description, "images": images, "variants": []any{}, "sourceUrl": in.URL, "notice": "Imported public page metadata. Review all fields before saving."})
}
