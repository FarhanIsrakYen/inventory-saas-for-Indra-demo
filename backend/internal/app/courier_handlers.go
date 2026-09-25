package app

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (a *API) createPathaoOrder(c *gin.Context) {
	userID, tenantID := ids(c)
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusNotFound, "ORDER_NOT_FOUND", "Order not found")
		return
	}
	result, err := a.orderService.SubmitCourierOrder(c.Request.Context(), tenantID, userID, orderID, pathaoProviderName)
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			fail(c, http.StatusNotFound, "ORDER_NOT_FOUND", "Order not found")
		case errors.Is(err, ErrOrderNotEligible):
			fail(c, http.StatusUnprocessableEntity, "ORDER_NOT_ELIGIBLE", "This order cannot be submitted to a courier")
		case errors.Is(err, ErrShipmentInProgress):
			fail(c, http.StatusConflict, "SHIPMENT_IN_PROGRESS", "Courier submission is already in progress")
		case errors.Is(err, ErrCourierNotConfigured):
			fail(c, http.StatusServiceUnavailable, "COURIER_NOT_CONFIGURED", "Pathao courier integration is not configured")
		default:
			var courierErr *CourierError
			if errors.As(err, &courierErr) {
				status := http.StatusBadGateway
				if courierErr.StatusCode >= 400 && courierErr.StatusCode < 500 {
					status = http.StatusUnprocessableEntity
				}
				fail(c, status, courierErr.Code, courierErr.Description)
				return
			}
			fail(c, http.StatusBadGateway, "COURIER_REQUEST_FAILED", "Unable to create courier order")
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Courier order created successfully", "description": "Order submitted to Pathao", "data": result})
}

func (a *API) getPathaoOrder(c *gin.Context) {
	_, tenantID := ids(c)
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusNotFound, "ORDER_NOT_FOUND", "Order not found")
		return
	}
	shipment, err := a.orderService.GetCourierShipment(tenantID, orderID, pathaoProviderName)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		ok(c, nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "SHIPMENT_LOOKUP_FAILED", "Unable to load courier shipment")
		return
	}
	ok(c, shipment)
}
