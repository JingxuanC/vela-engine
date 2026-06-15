// Package returns provides ShipEngine shipping label and tracking services.
package returns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const shipEngineBaseURL = "https://api.shipengine.com/v1"

// Address represents a physical shipping address.
type Address struct {
	Name       string
	Street1    string
	City       string
	State      string
	PostalCode string
	Country    string
}

// Dimensions represents package dimensions.
type Dimensions struct {
	Length float64
	Width  float64
	Height float64
	Unit   string
}

// LabelRequest is the input for creating a return shipping label.
type LabelRequest struct {
	ShipFrom          Address
	ShipTo            Address
	PackageWeight     float64
	PackageDimensions Dimensions
	ServiceCode       string
}

// LabelResponse is the result of a ShipEngine label creation.
type LabelResponse struct {
	LabelID        string
	TrackingNumber string
	LabelURL       string
	Cost           float64
	Currency       string
}

// ShipEngineClient is an HTTP client for the ShipEngine API.
type ShipEngineClient struct {
	apiKey string
	http   *http.Client
}

// NewShipEngineClient creates a new client with the given API key.
func NewShipEngineClient(apiKey string) *ShipEngineClient {
	return &ShipEngineClient{
		apiKey: apiKey,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:    20,
				IdleConnTimeout: 90 * time.Second,
			},
		},
	}
}

// ---- ShipEngine API types ----

type seAddress struct {
	Name          string `json:"name"`
	AddressLine1  string `json:"address_line1"`
	CityLocality  string `json:"city_locality"`
	StateProvince string `json:"state_province"`
	PostalCode    string `json:"postal_code"`
	CountryCode   string `json:"country_code"`
}

type seWeight struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

type seDimensions struct {
	Length float64 `json:"length"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Unit   string  `json:"unit"`
}

type sePackage struct {
	Weight     seWeight     `json:"weight"`
	Dimensions seDimensions `json:"dimensions"`
}

type seShipment struct {
	ServiceCode string      `json:"service_code"`
	ShipTo      seAddress   `json:"ship_to"`
	ShipFrom    seAddress   `json:"ship_from"`
	Packages    []sePackage `json:"packages"`
}

type seLabelRequest struct {
	Shipment    seShipment `json:"shipment"`
	LabelFormat string     `json:"label_format"`
	LabelLayout string     `json:"label_layout"`
}

type seLabelResponse struct {
	LabelID        string `json:"label_id"`
	TrackingNumber string `json:"tracking_number"`
	LabelDownload  *struct {
		PDF string `json:"pdf"`
	} `json:"label_download"`
	ShipmentCost *struct {
		Amount   float64 `json:"amount"`
		Currency string  `json:"currency"`
	} `json:"shipment_cost"`
	Status string `json:"status"`
}

// CreateLabel creates a return shipping label via ShipEngine.
func (c *ShipEngineClient) CreateLabel(ctx context.Context, req LabelRequest) (*LabelResponse, error) {
	body := seLabelRequest{
		Shipment: seShipment{
			ServiceCode: req.ServiceCode,
			ShipTo: seAddress{
				Name:          req.ShipTo.Name,
				AddressLine1:  req.ShipTo.Street1,
				CityLocality:  req.ShipTo.City,
				StateProvince: req.ShipTo.State,
				PostalCode:    req.ShipTo.PostalCode,
				CountryCode:   req.ShipTo.Country,
			},
			ShipFrom: seAddress{
				Name:          req.ShipFrom.Name,
				AddressLine1:  req.ShipFrom.Street1,
				CityLocality:  req.ShipFrom.City,
				StateProvince: req.ShipFrom.State,
				PostalCode:    req.ShipFrom.PostalCode,
				CountryCode:   req.ShipFrom.Country,
			},
			Packages: []sePackage{{
				Weight: seWeight{Value: req.PackageWeight, Unit: "pound"},
				Dimensions: seDimensions{
					Length: req.PackageDimensions.Length,
					Width:  req.PackageDimensions.Width,
					Height: req.PackageDimensions.Height,
					Unit:   req.PackageDimensions.Unit,
				},
			}},
		},
		LabelFormat: "pdf",
		LabelLayout: "4x6",
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("shipengine: marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", shipEngineBaseURL+"/labels", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("shipengine: create request: %w", err)
	}
	httpReq.Header.Set("API-Key", c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("shipengine: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("shipengine: read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("shipengine: HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 500))
	}

	var seResp seLabelResponse
	if err := json.Unmarshal(respBody, &seResp); err != nil {
		return nil, fmt.Errorf("shipengine: parse: %w", err)
	}

	result := &LabelResponse{
		LabelID:        seResp.LabelID,
		TrackingNumber: seResp.TrackingNumber,
	}
	if seResp.LabelDownload != nil {
		result.LabelURL = seResp.LabelDownload.PDF
	}
	if seResp.ShipmentCost != nil {
		result.Cost = seResp.ShipmentCost.Amount
		result.Currency = seResp.ShipmentCost.Currency
	}
	return result, nil
}

// Track queries tracking information for a tracking number.
func (c *ShipEngineClient) Track(ctx context.Context, trackingNumber string) (map[string]interface{}, error) {
	url := shipEngineBaseURL + "/tracking?tracking_number=" + trackingNumber
	httpReq, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	httpReq.Header.Set("API-Key", c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("shipengine track: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("shipengine: read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("shipengine track HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 500))
	}

	var result map[string]interface{}
	json.Unmarshal(respBody, &result)
	return result, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
