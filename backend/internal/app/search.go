package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

const productSearchIndex = "stockpilot-products"

type productSearchDocument struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenant_id"`
	Name        string `json:"name"`
	SKU         string `json:"sku"`
	Description string `json:"description"`
	VariantText string `json:"variant_text"`
}

func (a *API) elasticRequest(ctx context.Context, method, path string, payload any, result any) error {
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.elasticURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.elasticClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("elasticsearch returned %s", resp.Status)
	}
	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}

func (a *API) indexProduct(id, tenantID uuid.UUID) {
	if !a.elasticEnabled {
		return
	}
	var p Product
	if a.db.Where("id=? AND tenant_id=?", id, tenantID).Preload("Variants").First(&p).Error != nil {
		return
	}
	variantText := make([]string, 0, len(p.Variants)*3)
	for _, v := range p.Variants {
		variantText = append(variantText, v.SKU, v.Color, v.Size)
	}
	doc := productSearchDocument{ID: p.ID.String(), TenantID: p.TenantID.String(), Name: p.Name, SKU: p.SKU, Description: p.Description, VariantText: strings.Join(variantText, " ")}
	if err := a.elasticRequest(context.Background(), http.MethodPut, "/"+productSearchIndex+"/_doc/"+p.ID.String(), doc, nil); err != nil {
		a.log.Warn("elasticsearch product index failed", "product_id", p.ID, "error", err)
	}
}

func (a *API) rebuildProductIndex() {
	var products []Product
	if a.db.Preload("Variants").Find(&products).Error != nil {
		return
	}
	for _, p := range products {
		a.indexProduct(p.ID, p.TenantID)
	}
}

func (a *API) deleteProductIndex(ctx context.Context, id uuid.UUID) {
	if !a.elasticEnabled {
		return
	}
	if err := a.elasticRequest(ctx, http.MethodDelete, "/"+productSearchIndex+"/_doc/"+id.String(), nil, nil); err != nil {
		a.log.Warn("elasticsearch product removal failed", "product_id", id, "error", err)
	}
}

func (a *API) searchProductIDs(ctx context.Context, tenantID uuid.UUID, term string) ([]uuid.UUID, error) {
	var response struct {
		Hits struct {
			Hits []struct {
				Source productSearchDocument `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	payload := map[string]any{
		"size":    1000,
		"_source": []string{"id"},
		"query": map[string]any{"bool": map[string]any{
			"filter": []any{map[string]any{"term": map[string]any{"tenant_id": tenantID.String()}}},
			"must":   []any{map[string]any{"multi_match": map[string]any{"query": term, "fields": []string{"name^3", "sku^3", "description", "variant_text"}}}},
		}},
	}
	if err := a.elasticRequest(ctx, http.MethodPost, "/"+productSearchIndex+"/_search", payload, &response); err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(response.Hits.Hits))
	for _, hit := range response.Hits.Hits {
		if id, err := uuid.Parse(hit.Source.ID); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
