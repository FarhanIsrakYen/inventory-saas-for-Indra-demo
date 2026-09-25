package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const pathaoProviderName = "PATHAO"

var (
	ErrCourierNotConfigured = errors.New("courier provider is not configured")
	ErrShipmentInProgress   = errors.New("courier shipment submission is already in progress")
	ErrOrderNotEligible     = errors.New("order is not eligible for courier submission")
)

type CourierError struct {
	Code        string
	Description string
	StatusCode  int
	Temporary   bool
}

func (e *CourierError) Error() string { return e.Description }

type CreateCourierOrderRequest struct {
	MerchantOrderID    string
	RecipientName      string
	RecipientPhone     string
	RecipientAddress   string
	ItemQuantity       int
	ItemWeight         float64
	AmountToCollect    int64
	ItemDescription    string
	SpecialInstruction string
}

type CreateCourierOrderResponse struct {
	ProviderOrderID string
	TrackingNumber  string
	Status          string
	DeliveryFee     int64
	Raw             json.RawMessage
}

type CourierProvider interface {
	Name() string
	CreateOrder(ctx context.Context, req CreateCourierOrderRequest) (*CreateCourierOrderResponse, error)
}

type CourierService struct {
	providers map[string]CourierProvider
}

func NewCourierService(providers ...CourierProvider) *CourierService {
	registered := make(map[string]CourierProvider, len(providers))
	for _, provider := range providers {
		if provider != nil {
			registered[provider.Name()] = provider
		}
	}
	return &CourierService{providers: registered}
}

func (s *CourierService) CreateOrder(ctx context.Context, providerName string, req CreateCourierOrderRequest) (*CreateCourierOrderResponse, error) {
	provider, ok := s.providers[strings.ToUpper(providerName)]
	if !ok {
		return nil, ErrCourierNotConfigured
	}
	return provider.CreateOrder(ctx, req)
}

type OrderService struct {
	db      *gorm.DB
	courier *CourierService
}

func NewOrderService(db *gorm.DB, courier *CourierService) *OrderService {
	return &OrderService{db: db, courier: courier}
}

type CourierShipmentResponse struct {
	ShipmentID      uuid.UUID `json:"shipmentId"`
	Provider        string    `json:"provider"`
	ProviderOrderID string    `json:"providerOrderId"`
	TrackingNumber  string    `json:"trackingNumber"`
	Status          string    `json:"status"`
}

func (s *OrderService) SubmitCourierOrder(ctx context.Context, tenantID, userID, orderID uuid.UUID, providerName string) (*CourierShipmentResponse, error) {
	providerName = strings.ToUpper(providerName)
	var order Order
	if s.db.Where("id=? AND tenant_id=?", orderID, tenantID).Preload("Items").First(&order).Error != nil {
		return nil, gorm.ErrRecordNotFound
	}
	if order.Status == "Cancelled" || order.Status == "Returned" || len(order.Items) == 0 {
		return nil, ErrOrderNotEligible
	}
	if strings.TrimSpace(order.CustomerName) == "" || strings.TrimSpace(order.CustomerPhone) == "" || strings.TrimSpace(order.CustomerAddress) == "" {
		return nil, &CourierError{Code: "MISSING_SHIPPING_INFORMATION", Description: "Customer name, phone and delivery address are required"}
	}

	shipment, err := s.reserveShipment(tenantID, orderID, providerName)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, gorm.ErrRecordNotFound
		}
		if errors.Is(err, ErrShipmentInProgress) {
			return nil, ErrShipmentInProgress
		}
		return nil, err
	}
	if shipment.Status == "created" {
		return &CourierShipmentResponse{ShipmentID: shipment.ID, Provider: shipment.Provider, ProviderOrderID: shipment.ProviderOrderID, TrackingNumber: shipment.TrackingNumber, Status: shipment.Status}, nil
	}

	request := mapOrderToCourierRequest(order)
	payload, _ := json.Marshal(request)
	s.db.Model(&shipment).Update("request_payload", string(payload))

	response, err := s.courier.CreateOrder(ctx, providerName, request)
	if err != nil {
		s.db.Model(&shipment).Updates(map[string]any{"status": "failed", "response_payload": providerErrorPayload(err)})
		return nil, err
	}

	s.db.Model(&shipment).Updates(map[string]any{
		"provider_order_id": response.ProviderOrderID,
		"tracking_number":   response.TrackingNumber,
		"status":            response.Status,
		"delivery_fee":      response.DeliveryFee,
		"response_payload":  string(response.Raw),
	})
	auditMetadata := fmt.Sprintf(`{"provider":"%s","providerOrderId":"%s"}`, providerName, response.ProviderOrderID)
	s.db.Create(&AuditLog{Base: Base{ID: uuid.New()}, TenantID: tenantID, UserID: userID, Action: "courier.order.created", EntityType: "shipment", EntityID: shipment.ID.String(), Metadata: auditMetadata})

	var saved Shipment
	s.db.Where("id=? AND tenant_id=?", shipment.ID, tenantID).First(&saved)
	return &CourierShipmentResponse{ShipmentID: saved.ID, Provider: saved.Provider, ProviderOrderID: saved.ProviderOrderID, TrackingNumber: saved.TrackingNumber, Status: saved.Status}, nil
}

func (s *OrderService) GetCourierShipment(tenantID, orderID uuid.UUID, providerName string) (*CourierShipmentResponse, error) {
	var shipment Shipment
	if s.db.Where("tenant_id=? AND order_id=? AND provider=?", tenantID, orderID, strings.ToUpper(providerName)).First(&shipment).Error != nil {
		return nil, gorm.ErrRecordNotFound
	}
	return &CourierShipmentResponse{ShipmentID: shipment.ID, Provider: shipment.Provider, ProviderOrderID: shipment.ProviderOrderID, TrackingNumber: shipment.TrackingNumber, Status: shipment.Status}, nil
}

func mapOrderToCourierRequest(order Order) CreateCourierOrderRequest {
	quantity := 0
	items := make([]string, 0, len(order.Items))
	for _, item := range order.Items {
		quantity += item.Quantity
		items = append(items, fmt.Sprintf("%s (%s) x%d", item.ProductName, item.VariantName, item.Quantity))
	}
	weight := float64(quantity) * 0.5
	if weight < 0.5 {
		weight = 0.5
	}
	return CreateCourierOrderRequest{MerchantOrderID: order.Number, RecipientName: order.CustomerName, RecipientPhone: order.CustomerPhone, RecipientAddress: order.CustomerAddress, ItemQuantity: quantity, ItemWeight: weight, AmountToCollect: order.Total, ItemDescription: strings.Join(items, "; "), SpecialInstruction: order.Notes}
}

func (s *OrderService) reserveShipment(tenantID, orderID uuid.UUID, provider string) (Shipment, error) {
	tx := s.db.Begin()
	if tx.Error != nil {
		return Shipment{}, tx.Error
	}
	var shipment Shipment
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND order_id=? AND provider=?", tenantID, orderID, provider).First(&shipment).Error
	if err == nil {
		if shipment.Status == "created" {
			tx.Commit()
			return shipment, nil
		}
		if shipment.Status == "submitting" {
			tx.Commit()
			return Shipment{}, ErrShipmentInProgress
		}
		shipment.Status = "submitting"
		if tx.Save(&shipment).Error != nil {
			tx.Rollback()
			return Shipment{}, errors.New("unable to reserve courier shipment")
		}
		tx.Commit()
		return shipment, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		tx.Rollback()
		return Shipment{}, err
	}
	shipment = Shipment{Base: Base{ID: uuid.New()}, TenantID: tenantID, OrderID: orderID, Provider: provider, Status: "submitting"}
	if tx.Create(&shipment).Error != nil {
		tx.Rollback()
		var existing Shipment
		if s.db.Where("tenant_id=? AND order_id=? AND provider=?", tenantID, orderID, provider).First(&existing).Error == nil {
			if existing.Status == "created" {
				return existing, nil
			}
			return Shipment{}, ErrShipmentInProgress
		}
		return Shipment{}, errors.New("unable to reserve courier shipment")
	}
	if tx.Commit().Error != nil {
		return Shipment{}, errors.New("unable to reserve courier shipment")
	}
	return shipment, nil
}

func providerErrorPayload(err error) string {
	var courierErr *CourierError
	if errors.As(err, &courierErr) {
		payload, _ := json.Marshal(map[string]any{"code": courierErr.Code, "description": courierErr.Description, "statusCode": courierErr.StatusCode})
		return string(payload)
	}
	return `{"description":"courier request failed"}`
}

type PathaoConfig struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	Username     string
	Password     string
	StoreID      int
}

type PathaoProvider struct {
	config PathaoConfig
	client *http.Client
	log    *slog.Logger
	mu     sync.Mutex
	token  pathaoToken
}

type pathaoToken struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

func NewPathaoProvider(config PathaoConfig, client *http.Client) *PathaoProvider {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	return &PathaoProvider{config: config, client: client, log: slog.Default()}
}

func (p *PathaoProvider) Name() string { return pathaoProviderName }

func (p *PathaoProvider) CreateOrder(ctx context.Context, req CreateCourierOrderRequest) (*CreateCourierOrderResponse, error) {
	p.log.Info("pathao.order.create.started", "merchant_order_id", req.MerchantOrderID)
	if p.config.BaseURL == "" || p.config.ClientID == "" || p.config.ClientSecret == "" || p.config.Username == "" || p.config.Password == "" || p.config.StoreID == 0 {
		p.log.Warn("pathao.order.create.failed", "merchant_order_id", req.MerchantOrderID, "reason", "not_configured")
		return nil, ErrCourierNotConfigured
	}
	token, err := p.accessToken(ctx)
	if err != nil {
		p.log.Warn("pathao.order.create.failed", "merchant_order_id", req.MerchantOrderID, "reason", "authentication")
		return nil, err
	}
	response, status, raw, err := p.createOrder(ctx, token, req)
	if status == http.StatusUnauthorized {
		p.invalidateToken(token)
		token, err = p.accessToken(ctx)
		if err == nil {
			response, status, raw, err = p.createOrder(ctx, token, req)
		}
	}
	if err != nil {
		p.log.Warn("pathao.order.create.failed", "merchant_order_id", req.MerchantOrderID, "reason", "request")
		return nil, err
	}
	if status < 200 || status >= 300 {
		p.log.Warn("pathao.order.create.failed", "merchant_order_id", req.MerchantOrderID, "status_code", status)
		return nil, pathaoAPIError(status, raw)
	}
	if response.ProviderOrderID == "" {
		p.log.Warn("pathao.order.create.failed", "merchant_order_id", req.MerchantOrderID, "reason", "malformed_response")
		return nil, &CourierError{Code: "MALFORMED_RESPONSE", Description: "Pathao returned no consignment ID", StatusCode: status}
	}
	p.log.Info("pathao.order.create.success", "merchant_order_id", req.MerchantOrderID, "provider_order_id", response.ProviderOrderID)
	return response, nil
}

func (p *PathaoProvider) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token.AccessToken != "" && time.Now().Before(p.token.ExpiresAt.Add(-time.Minute)) {
		return p.token.AccessToken, nil
	}
	payload := map[string]any{"client_id": p.config.ClientID, "client_secret": p.config.ClientSecret, "grant_type": "password", "username": p.config.Username, "password": p.config.Password}
	if p.token.RefreshToken != "" {
		payload = map[string]any{"client_id": p.config.ClientID, "client_secret": p.config.ClientSecret, "grant_type": "refresh_token", "refresh_token": p.token.RefreshToken}
	}
	if payload["grant_type"] == "refresh_token" {
		p.log.Info("pathao.auth.refresh")
	}
	response, status, raw, err := p.issueToken(ctx, payload)
	if (err != nil || status < 200 || status >= 300 || response.Data.AccessToken == "") && payload["grant_type"] == "refresh_token" {
		payload = map[string]any{"client_id": p.config.ClientID, "client_secret": p.config.ClientSecret, "grant_type": "password", "username": p.config.Username, "password": p.config.Password}
		response, status, raw, err = p.issueToken(ctx, payload)
	}
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 || response.Data.AccessToken == "" {
		p.token = pathaoToken{}
		return "", pathaoAPIError(status, raw)
	}
	expires := time.Duration(response.Data.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = 2 * time.Hour
	}
	p.token = pathaoToken{AccessToken: response.Data.AccessToken, RefreshToken: response.Data.RefreshToken, ExpiresAt: time.Now().Add(expires)}
	return p.token.AccessToken, nil
}

func (p *PathaoProvider) issueToken(ctx context.Context, payload map[string]any) (pathaoTokenEnvelope, int, []byte, error) {
	var response pathaoTokenEnvelope
	status, raw, err := p.request(ctx, http.MethodPost, "/aladdin/api/v1/issue-token", "", payload, &response)
	return response, status, raw, err
}

func (p *PathaoProvider) invalidateToken(accessToken string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token.AccessToken == accessToken {
		p.token.ExpiresAt = time.Time{}
	}
}

func (p *PathaoProvider) createOrder(ctx context.Context, token string, req CreateCourierOrderRequest) (*CreateCourierOrderResponse, int, []byte, error) {
	payload := map[string]any{"store_id": p.config.StoreID, "merchant_order_id": req.MerchantOrderID, "recipient_name": req.RecipientName, "recipient_phone": req.RecipientPhone, "recipient_address": req.RecipientAddress, "delivery_type": 48, "item_type": 2, "item_quantity": req.ItemQuantity, "item_weight": strconv.FormatFloat(req.ItemWeight, 'f', 2, 64), "amount_to_collect": req.AmountToCollect, "item_description": req.ItemDescription, "special_instruction": req.SpecialInstruction}
	var response pathaoOrderEnvelope
	status, raw, err := p.request(ctx, http.MethodPost, "/aladdin/api/v1/orders", token, payload, &response)
	if err != nil {
		return nil, status, raw, err
	}
	return &CreateCourierOrderResponse{ProviderOrderID: response.Data.ConsignmentID, TrackingNumber: response.Data.ConsignmentID, Status: strings.ToLower(response.Data.OrderStatus), DeliveryFee: response.Data.DeliveryFee, Raw: raw}, status, raw, nil
}

func (p *PathaoProvider) request(ctx context.Context, method, path, token string, payload any, output any) (int, []byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, p.config.BaseURL+path, strings.NewReader(string(body)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return 0, nil, &CourierError{Code: "COURIER_TIMEOUT", Description: "Courier request timed out", Temporary: true}
		}
		return 0, nil, &CourierError{Code: "COURIER_CONNECTION_FAILED", Description: "Unable to connect to courier provider", Temporary: true}
	}
	defer resp.Body.Close()
	var rawData json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&rawData); err != nil {
		return resp.StatusCode, nil, &CourierError{Code: "MALFORMED_RESPONSE", Description: "Courier returned an invalid response", StatusCode: resp.StatusCode}
	}
	if output != nil && len(rawData) > 0 {
		if err := json.Unmarshal(rawData, output); err != nil {
			return resp.StatusCode, rawData, &CourierError{Code: "MALFORMED_RESPONSE", Description: "Courier returned an invalid response", StatusCode: resp.StatusCode}
		}
	}
	return resp.StatusCode, rawData, nil
}

type pathaoTokenEnvelope struct {
	Data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	} `json:"data"`
}

type pathaoOrderEnvelope struct {
	Data struct {
		ConsignmentID string `json:"consignment_id"`
		InvoiceID     string `json:"invoice_id"`
		OrderStatus   string `json:"order_status"`
		DeliveryFee   int64  `json:"delivery_fee"`
	} `json:"data"`
}

func pathaoAPIError(status int, raw []byte) error {
	description := "Pathao rejected the courier request"
	if status >= 500 {
		description = "Pathao is temporarily unavailable"
	}
	var body struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &body) == nil && body.Message != "" && len(body.Message) < 300 {
		description = body.Message
	}
	return &CourierError{Code: "PATHAO_API_ERROR", Description: description, StatusCode: status, Temporary: status >= 500}
}
