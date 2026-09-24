package app

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func (a *API) dashboard(c *gin.Context) {
	_, t := ids(c)
	var products, inventory, low, orders int64
	var revenue int64
	var lowProducts []Product
	a.db.Where("tenant_id=?", t).Preload("Variants", "quantity < ?", 5).Where("id IN (SELECT product_id FROM product_variants WHERE tenant_id=? AND quantity < ?)", t, 5).Limit(8).Find(&lowProducts)
	a.db.Model(&Product{}).Where("tenant_id=?", t).Count(&products)
	a.db.Model(&Variant{}).Where("tenant_id=?", t).Select("COALESCE(SUM(quantity),0)").Scan(&inventory)
	a.db.Model(&Variant{}).Where("tenant_id=? AND quantity < ?", t, 5).Count(&low)
	today := time.Now().Truncate(24 * time.Hour)
	a.db.Model(&Order{}).Where("tenant_id=? AND created_at >= ?", t, today).Count(&orders)
	a.db.Model(&Order{}).Where("tenant_id=? AND created_at >= ? AND status != ?", t, today, "Cancelled").Select("COALESCE(SUM(total),0)").Scan(&revenue)
	var recent []Order
	a.db.Where("tenant_id=?", t).Order("created_at desc").Limit(5).Find(&recent)
	ok(c, gin.H{"totalProducts": products, "totalInventory": inventory, "lowStock": low, "lowStockProducts": lowProducts, "todayOrders": orders, "todayRevenue": revenue, "recentOrders": recent})
}

type adjustmentInput struct {
	VariantID uuid.UUID `json:"variantId" binding:"required"`
	Change    int       `json:"change" binding:"required"`
	Reason    string    `json:"reason" binding:"required"`
}

func (a *API) adjustStock(c *gin.Context) {
	var in adjustmentInput
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "VALIDATION_ERROR", "Variant, change and reason are required")
		return
	}
	u, t := ids(c)
	tx := a.db.Begin()
	var v Variant
	if tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=?", in.VariantID, t).First(&v).Error != nil {
		tx.Rollback()
		fail(c, 404, "VARIANT_NOT_FOUND", "Variant not found")
		return
	}
	next := v.Quantity + in.Change
	if next < 0 {
		tx.Rollback()
		fail(c, 422, "INSUFFICIENT_STOCK", "This adjustment would make inventory negative")
		return
	}
	tx.Model(&v).Update("quantity", next)
	tx.Create(&InventoryTransaction{Base: Base{ID: uuid.New()}, TenantID: t, VariantID: v.ID, UserID: u, PreviousQuantity: v.Quantity, Change: in.Change, NewQuantity: next, Reason: in.Reason})
	tx.Commit()
	a.audit(t, u, "inventory.adjusted", "variant", v.ID.String())
	ok(c, gin.H{"quantity": next})
}
func (a *API) listTransactions(c *gin.Context) {
	_, t := ids(c)
	var x []InventoryTransaction
	a.db.Where("tenant_id=?", t).Order("created_at desc").Limit(100).Find(&x)
	ok(c, x)
}
func (a *API) listOrders(c *gin.Context) {
	_, t := ids(c)
	q := a.db.Where("tenant_id=?", t).Preload("Items")
	if x := c.Query("status"); x != "" {
		q = q.Where("status=?", x)
	}
	if x := c.Query("search"); x != "" {
		q = q.Where("number ILIKE ? OR customer_name ILIKE ? OR customer_phone ILIKE ?", "%"+x+"%", "%"+x+"%", "%"+x+"%")
	}
	var x []Order
	var n int64
	q.Model(&Order{}).Count(&n)
	p, s := pageArgs(c)
	q.Order("created_at desc").Offset((p - 1) * s).Limit(s).Find(&x)
	ok(c, gin.H{"items": x, "total": n, "page": p, "size": s})
}

type orderLine struct {
	VariantID uuid.UUID `json:"variantId" binding:"required"`
	Quantity  int       `json:"quantity" binding:"gt=0"`
}
type orderInput struct {
	CustomerName     string      `json:"customerName" binding:"required"`
	CustomerPhone    string      `json:"customerPhone" binding:"required,min=8"`
	CustomerAddress  string      `json:"customerAddress" binding:"required"`
	Notes            string      `json:"notes"`
	DeliveryOptionID uuid.UUID   `json:"deliveryOptionId" binding:"required"`
	Items            []orderLine `json:"items" binding:"required,min=1"`
}

func (a *API) createOrder(c *gin.Context) {
	var in orderInput
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "VALIDATION_ERROR", "Check customer, delivery and order item details")
		return
	}
	u, t := ids(c)
	tx := a.db.Begin()
	var d DeliveryOption
	if tx.Where("id=? AND tenant_id=? AND enabled=?", in.DeliveryOptionID, t, true).First(&d).Error != nil {
		tx.Rollback()
		fail(c, 422, "DELIVERY_OPTION_NOT_FOUND", "Choose an enabled delivery option")
		return
	}
	o := Order{Base: Base{ID: uuid.New()}, TenantID: t, Number: "ORD-" + time.Now().Format("20060102-150405"), CustomerName: in.CustomerName, CustomerPhone: in.CustomerPhone, CustomerAddress: in.CustomerAddress, Notes: in.Notes, Status: "Pending", Currency: d.Currency, DeliveryOptionID: &d.ID, DeliveryName: d.Name, DeliveryCharge: d.Price}
	for _, line := range in.Items {
		var v Variant
		if tx.Clauses(clause.Locking{Strength: "UPDATE"}).Preload("Images").Where("id=? AND tenant_id=?", line.VariantID, t).First(&v).Error != nil {
			tx.Rollback()
			fail(c, 404, "VARIANT_NOT_FOUND", "One of the selected variants was not found")
			return
		}
		if v.Quantity < line.Quantity {
			tx.Rollback()
			fail(c, 422, "INSUFFICIENT_STOCK", "Insufficient inventory for one or more items")
			return
		}
		var p Product
		tx.Where("id=? AND tenant_id=?", v.ProductID, t).First(&p)
		price := choosePrice(v.Price, p.Price)
		o.Subtotal += price * int64(line.Quantity)
		tx.Model(&v).Update("quantity", v.Quantity-line.Quantity)
		tx.Create(&InventoryTransaction{Base: Base{ID: uuid.New()}, TenantID: t, VariantID: v.ID, UserID: u, PreviousQuantity: v.Quantity, Change: -line.Quantity, NewQuantity: v.Quantity - line.Quantity, Reason: "Order"})
		o.Items = append(o.Items, OrderItem{Base: Base{ID: uuid.New()}, TenantID: t, VariantID: v.ID, ProductName: p.Name, VariantName: fmt.Sprintf("%s / %s", v.Color, v.Size), ProductImageURL: firstImage(v.Images), Quantity: line.Quantity, UnitPrice: price, Subtotal: price * int64(line.Quantity)})
	}
	o.Total = o.Subtotal + o.DeliveryCharge
	if e := tx.Create(&o).Error; e != nil {
		tx.Rollback()
		fail(c, 500, "ORDER_CREATE_FAILED", "Unable to create order")
		return
	}
	tx.Commit()
	a.audit(t, u, "order.created", "order", o.ID.String())
	ok(c, o)
}
func (a *API) order(c *gin.Context) {
	_, t := ids(c)
	id, e := uuid.Parse(c.Param("id"))
	if e != nil {
		fail(c, 404, "ORDER_NOT_FOUND", "Order not found")
		return
	}
	var o Order
	if a.db.Where("id=? AND tenant_id=?", id, t).Preload("Items").First(&o).Error != nil {
		fail(c, 404, "ORDER_NOT_FOUND", "Order not found")
		return
	}
	for i := range o.Items {
		if o.Items[i].ProductImageURL != "" {
			continue
		}
		var v Variant
		if a.db.Where("id=? AND tenant_id=?", o.Items[i].VariantID, t).Preload("Images").First(&v).Error == nil {
			o.Items[i].ProductImageURL = firstImage(v.Images)
			if o.Items[i].ProductImageURL == "" {
				var p Product
				if a.db.Where("id=? AND tenant_id=?", v.ProductID, t).Preload("Images").First(&p).Error == nil && len(p.Images) > 0 {
					o.Items[i].ProductImageURL = p.Images[0].URL
				}
			}
		}
	}
	ok(c, o)
}
func (a *API) updateOrderStatus(c *gin.Context) {
	u, t := ids(c)
	id, e := uuid.Parse(c.Param("id"))
	if e != nil {
		fail(c, 404, "ORDER_NOT_FOUND", "Order not found")
		return
	}
	var in struct {
		Status string `json:"status" binding:"required,oneof=Pending Confirmed Processing Shipped Delivered Cancelled Returned"`
	}
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "VALIDATION_ERROR", "Invalid order status")
		return
	}
	if a.db.Model(&Order{}).Where("id=? AND tenant_id=?", id, t).Update("status", in.Status).RowsAffected == 0 {
		fail(c, 404, "ORDER_NOT_FOUND", "Order not found")
		return
	}
	a.audit(t, u, "order.status_changed", "order", id.String())
	ok(c, gin.H{"status": in.Status})
}
func (a *API) updateOrder(c *gin.Context) {
	u, t := ids(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, 404, "ORDER_NOT_FOUND", "Order not found")
		return
	}
	var in struct {
		CustomerName     string      `json:"customerName" binding:"required"`
		CustomerPhone    string      `json:"customerPhone" binding:"required,min=8"`
		CustomerAddress  string      `json:"customerAddress" binding:"required"`
		Notes            string      `json:"notes"`
		Status           string      `json:"status" binding:"omitempty,oneof=Pending Confirmed Processing Shipped Delivered Cancelled Returned"`
		DeliveryOptionID uuid.UUID   `json:"deliveryOptionId" binding:"required"`
		Items            []orderLine `json:"items" binding:"required,min=1"`
	}
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "VALIDATION_ERROR", "Check customer, delivery and order item details")
		return
	}
	tx := a.db.Begin()
	var o Order
	if tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=?", id, t).Preload("Items").First(&o).Error != nil {
		tx.Rollback()
		fail(c, 404, "ORDER_NOT_FOUND", "Order not found")
		return
	}
	var d DeliveryOption
	if tx.Where("id=? AND tenant_id=? AND enabled=?", in.DeliveryOptionID, t, true).First(&d).Error != nil {
		tx.Rollback()
		fail(c, 422, "DELIVERY_OPTION_NOT_FOUND", "Choose an enabled delivery option")
		return
	}
	oldQuantities := map[uuid.UUID]int{}
	for _, item := range o.Items {
		oldQuantities[item.VariantID] += item.Quantity
	}
	newQuantities := map[uuid.UUID]int{}
	for _, line := range in.Items {
		newQuantities[line.VariantID] += line.Quantity
	}
	sameItems := len(oldQuantities) == len(newQuantities)
	if sameItems {
		for variantID, quantity := range oldQuantities {
			if newQuantities[variantID] != quantity {
				sameItems = false
				break
			}
		}
	}
	if sameItems {
		o.CustomerName, o.CustomerPhone, o.CustomerAddress, o.Notes = in.CustomerName, in.CustomerPhone, in.CustomerAddress, in.Notes
		o.Status, o.DeliveryOptionID, o.DeliveryName, o.DeliveryCharge, o.Currency = in.Status, &d.ID, d.Name, d.Price, d.Currency
		if o.Status == "" {
			o.Status = "Pending"
		}
		if tx.Save(&o).Error != nil || tx.Commit().Error != nil {
			tx.Rollback()
			fail(c, 500, "ORDER_UPDATE_FAILED", "Unable to update order")
			return
		}
		a.audit(t, u, "order.updated", "order", id.String())
		a.order(c)
		return
	}
	for _, item := range o.Items {
		if tx.Model(&Variant{}).Where("id=? AND tenant_id=?", item.VariantID, t).UpdateColumn("quantity", gorm.Expr("quantity + ?", item.Quantity)).RowsAffected == 0 {
			tx.Rollback()
			fail(c, 404, "VARIANT_NOT_FOUND", "An existing order variant was not found")
			return
		}
	}
	o.CustomerName, o.CustomerPhone, o.CustomerAddress, o.Notes = in.CustomerName, in.CustomerPhone, in.CustomerAddress, in.Notes
	o.Status, o.DeliveryOptionID, o.DeliveryName, o.DeliveryCharge, o.Currency = in.Status, &d.ID, d.Name, d.Price, d.Currency
	if o.Status == "" {
		o.Status = "Pending"
	}
	o.Subtotal = 0
	o.Items = nil
	for _, line := range in.Items {
		var v Variant
		if tx.Clauses(clause.Locking{Strength: "UPDATE"}).Preload("Images").Where("id=? AND tenant_id=?", line.VariantID, t).First(&v).Error != nil {
			tx.Rollback()
			fail(c, 404, "VARIANT_NOT_FOUND", "One of the selected variants was not found")
			return
		}
		if v.Quantity < line.Quantity {
			tx.Rollback()
			fail(c, 422, "INSUFFICIENT_STOCK", "Insufficient inventory for one or more items")
			return
		}
		var p Product
		if tx.Where("id=? AND tenant_id=?", v.ProductID, t).First(&p).Error != nil {
			tx.Rollback()
			fail(c, 404, "PRODUCT_NOT_FOUND", "Product not found")
			return
		}
		price := choosePrice(v.Price, p.Price)
		tx.Model(&v).Update("quantity", v.Quantity-line.Quantity)
		tx.Create(&InventoryTransaction{Base: Base{ID: uuid.New()}, TenantID: t, VariantID: v.ID, UserID: u, PreviousQuantity: v.Quantity, Change: -line.Quantity, NewQuantity: v.Quantity - line.Quantity, Reason: "Order updated"})
		o.Subtotal += price * int64(line.Quantity)
		o.Items = append(o.Items, OrderItem{Base: Base{ID: uuid.New()}, TenantID: t, OrderID: o.ID, VariantID: v.ID, ProductName: p.Name, VariantName: fmt.Sprintf("%s / %s", v.Color, v.Size), ProductImageURL: firstImage(v.Images), Quantity: line.Quantity, UnitPrice: price, Subtotal: price * int64(line.Quantity)})
	}
	o.Total = o.Subtotal + o.DeliveryCharge
	if tx.Where("order_id=? AND tenant_id=?", id, t).Delete(&OrderItem{}).Error != nil || tx.Save(&o).Error != nil || tx.Create(&o.Items).Error != nil {
		tx.Rollback()
		fail(c, 500, "ORDER_UPDATE_FAILED", "Unable to update order")
		return
	}
	if tx.Commit().Error != nil {
		fail(c, 500, "ORDER_UPDATE_FAILED", "Unable to update order")
		return
	}
	a.audit(t, u, "order.updated", "order", id.String())
	a.order(c)
}

func (a *API) deleteOrder(c *gin.Context) {
	u, t := ids(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, 404, "ORDER_NOT_FOUND", "Order not found")
		return
	}
	tx := a.db.Begin()
	var o Order
	if tx.Where("id=? AND tenant_id=?", id, t).Preload("Items").First(&o).Error != nil {
		tx.Rollback()
		fail(c, 404, "ORDER_NOT_FOUND", "Order not found")
		return
	}
	for _, item := range o.Items {
		tx.Model(&Variant{}).Where("id=? AND tenant_id=?", item.VariantID, t).UpdateColumn("quantity", gorm.Expr("quantity + ?", item.Quantity))
	}
	tx.Where("order_id=? AND tenant_id=?", id, t).Delete(&OrderItem{})
	tx.Delete(&o)
	tx.Commit()
	a.audit(t, u, "order.deleted", "order", id.String())
	c.Status(204)
}
func (a *API) listDelivery(c *gin.Context) {
	_, t := ids(c)
	var x []DeliveryOption
	a.db.Where("tenant_id=?", t).Order("price asc").Find(&x)
	ok(c, x)
}
func (a *API) createDelivery(c *gin.Context) {
	_, t := ids(c)
	var x DeliveryOption
	if c.ShouldBindJSON(&x) != nil || x.Name == "" || x.Price < 0 {
		fail(c, 400, "VALIDATION_ERROR", "Enter a valid delivery option")
		return
	}
	x.Base = Base{ID: uuid.New()}
	x.TenantID = t
	x.Currency = currency(x.Currency)
	x.Enabled = true
	a.db.Create(&x)
	ok(c, x)
}
func (a *API) updateDelivery(c *gin.Context) {
	_, t := ids(c)
	id, e := uuid.Parse(c.Param("id"))
	var x DeliveryOption
	if e != nil || a.db.Where("id=? AND tenant_id=?", id, t).First(&x).Error != nil {
		fail(c, 404, "DELIVERY_OPTION_NOT_FOUND", "Delivery option not found")
		return
	}
	var in DeliveryOption
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "VALIDATION_ERROR", "Enter a valid delivery option")
		return
	}
	x.Name = in.Name
	x.Price = in.Price
	x.Enabled = in.Enabled
	x.Currency = currency(in.Currency)
	a.db.Save(&x)
	ok(c, x)
}
func (a *API) deleteDelivery(c *gin.Context) {
	_, t := ids(c)
	id, e := uuid.Parse(c.Param("id"))
	if e != nil || a.db.Where("id=? AND tenant_id=?", id, t).Delete(&DeliveryOption{}).RowsAffected == 0 {
		fail(c, 404, "DELIVERY_OPTION_NOT_FOUND", "Delivery option not found")
		return
	}
	c.Status(204)
}
func (a *API) auditLogs(c *gin.Context) {
	_, t := ids(c)
	var x []AuditLog
	a.db.Where("tenant_id=?", t).Order("created_at desc").Limit(100).Find(&x)
	ok(c, x)
}

var _ = gorm.ErrRecordNotFound
