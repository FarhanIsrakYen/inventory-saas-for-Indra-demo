package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMapOrderToCourierRequest(t *testing.T) {
	order := Order{Number: "ORD-1", CustomerName: "Jane", CustomerPhone: "01700000000", CustomerAddress: "House 1, Dhaka", Notes: "Call first", Total: 1250, Items: []OrderItem{{ProductName: "Shirt", VariantName: "Black / M", Quantity: 2}}}
	got := mapOrderToCourierRequest(order)
	if got.MerchantOrderID != "ORD-1" || got.RecipientPhone != "01700000000" || got.ItemQuantity != 2 || got.AmountToCollect != 1250 || got.ItemWeight != 1 {
		t.Fatalf("unexpected courier mapping: %+v", got)
	}
	if !strings.Contains(got.ItemDescription, "Shirt") || got.SpecialInstruction != "Call first" {
		t.Fatalf("missing mapped item or instruction: %+v", got)
	}
}

func TestPathaoProviderCachesTokenAndCreatesOrder(t *testing.T) {
	var authCalls atomic.Int32
	var orderCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/aladdin/api/v1/issue-token":
			authCalls.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600}})
		case "/aladdin/api/v1/orders":
			orderCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer access-1" {
				t.Error("missing bearer token")
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"consignment_id": "C-100", "order_status": "Pending", "delivery_fee": 80}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := NewPathaoProvider(PathaoConfig{BaseURL: server.URL, ClientID: "id", ClientSecret: "secret", Username: "user", Password: "pass", StoreID: 10}, server.Client())
	request := CreateCourierOrderRequest{MerchantOrderID: "ORD-1", RecipientName: "Jane", RecipientPhone: "01700000000", RecipientAddress: "Dhaka", ItemQuantity: 1, ItemWeight: .5, AmountToCollect: 100}
	for range 2 {
		response, err := provider.CreateOrder(context.Background(), request)
		if err != nil || response.ProviderOrderID != "C-100" || response.TrackingNumber != "C-100" {
			t.Fatalf("unexpected provider response: %+v, %v", response, err)
		}
	}
	if authCalls.Load() != 1 || orderCalls.Load() != 2 {
		t.Fatalf("expected one auth call and two order calls, got %d and %d", authCalls.Load(), orderCalls.Load())
	}
}

func TestPathaoProviderRefreshFailureFallsBackToPasswordAuth(t *testing.T) {
	var authCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/aladdin/api/v1/issue-token" {
			call := authCalls.Add(1)
			if call == 2 {
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]any{"message": "refresh expired"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"access_token": "access-" + string(rune('0'+call)), "refresh_token": "refresh", "expires_in": 3600}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"consignment_id": "C-1", "order_status": "Pending"}})
	}))
	defer server.Close()
	provider := NewPathaoProvider(PathaoConfig{BaseURL: server.URL, ClientID: "id", ClientSecret: "secret", Username: "user", Password: "pass", StoreID: 10}, server.Client())
	request := CreateCourierOrderRequest{MerchantOrderID: "ORD-1", RecipientName: "Jane", RecipientPhone: "01700000000", RecipientAddress: "Dhaka", ItemQuantity: 1, ItemWeight: .5}
	if _, err := provider.CreateOrder(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	provider.token.ExpiresAt = time.Now().Add(-time.Hour)
	if _, err := provider.CreateOrder(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if authCalls.Load() != 3 {
		t.Fatalf("expected password auth fallback after refresh failure, got %d auth calls", authCalls.Load())
	}
}

func TestPathaoProviderMapsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/aladdin/api/v1/issue-token" {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"access_token": "access", "expires_in": 3600}})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"message": "invalid recipient"})
	}))
	defer server.Close()
	provider := NewPathaoProvider(PathaoConfig{BaseURL: server.URL, ClientID: "id", ClientSecret: "secret", Username: "user", Password: "pass", StoreID: 10}, server.Client())
	_, err := provider.CreateOrder(context.Background(), CreateCourierOrderRequest{MerchantOrderID: "ORD-1"})
	var courierErr *CourierError
	if !errors.As(err, &courierErr) || courierErr.StatusCode != http.StatusBadRequest || courierErr.Description != "invalid recipient" {
		t.Fatalf("unexpected mapped error: %#v", err)
	}
}
