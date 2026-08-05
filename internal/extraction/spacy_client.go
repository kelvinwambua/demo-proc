package extraction

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type SpaCyResult struct {
	CustomerName        *string            `json:"customer_name"`
	CargoFrom           *string            `json:"cargo_from"`
	Destination         *string            `json:"destination"`
	MobilizationPoint   *string            `json:"mobilization_point"`
	CargoType           *string            `json:"cargo_type"`
	WeightVolume        *string            `json:"weight_volume"`
	DeliveryUrgency     *string            `json:"delivery_urgency"`
	SpecialHandling     *string            `json:"special_handling"`
	DistanceKMRoundTrip *float64           `json:"distance_km_roundtrip"`
	AvgKMPerDay         *float64           `json:"avg_km_per_day"`
	LoadingDays         *float64           `json:"loading_days"`
	Confidence          map[string]float64 `json:"confidence"`
	Unresolved          []string           `json:"unresolved"`
}

type SpaCyClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewSpaCyClient(baseURL string) *SpaCyClient {
	return &SpaCyClient{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *SpaCyClient) Extract(ctx context.Context, text string) (*SpaCyResult, error) {
	url := c.BaseURL + "/extract"

	payload := map[string]string{"text": text}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call spacy sidecar: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read spacy response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spacy sidecar returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result SpaCyResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse spacy response: %w (body: %s)", err, string(respBody))
	}

	if result.Confidence == nil {
		result.Confidence = map[string]float64{}
	}
	if result.Unresolved == nil {
		result.Unresolved = []string{}
	}

	return &result, nil
}
