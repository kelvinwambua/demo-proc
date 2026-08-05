package extraction

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const apiVersion = "2024-11-30"

type Client struct {
	Endpoint   string
	Key        string
	HTTPClient *http.Client
}

func NewClient(endpoint, key string) *Client {
	return &Client{
		Endpoint:   strings.TrimRight(endpoint, "/"),
		Key:        key,
		HTTPClient: &http.Client{Timeout: 60 * time.Second},
	}
}

type Result struct {
	RawText string          `json:"raw_text"`
	RawJSON json.RawMessage `json:"raw_json"`
}

func (c *Client) Analyze(ctx context.Context, docBytes []byte) (*Result, error) {
	opLocation, err := c.startAnalyze(ctx, docBytes)
	if err != nil {
		return nil, fmt.Errorf("start analyze: %w", err)
	}

	raw, err := c.poll(ctx, opLocation)
	if err != nil {
		return nil, fmt.Errorf("poll analyze result: %w", err)
	}

	return parseResult(raw)
}

func (c *Client) startAnalyze(ctx context.Context, docBytes []byte) (string, error) {
	url := fmt.Sprintf("%s/documentintelligence/documentModels/prebuilt-read:analyze?api-version=%s",
		c.Endpoint, apiVersion)

	body := map[string]string{"base64Source": base64.StdEncoding.EncodeToString(docBytes)}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Ocp-Apim-Subscription-Key", c.Key)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("azure DI returned %d: %s", resp.StatusCode, string(b))
	}

	opLocation := resp.Header.Get("Operation-Location")
	if opLocation == "" {
		return "", fmt.Errorf("azure DI response missing Operation-Location header")
	}
	return opLocation, nil
}

func (c *Client) poll(ctx context.Context, opLocation string) (json.RawMessage, error) {
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, opLocation, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Ocp-Apim-Subscription-Key", c.Key)

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		var envelope struct {
			Status string          `json:"status"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(b, &envelope); err != nil {
			return nil, fmt.Errorf("unexpected DI poll response: %s", string(b))
		}

		switch envelope.Status {
		case "succeeded":
			return b, nil
		case "failed":
			return nil, fmt.Errorf("azure DI analysis failed: %s", string(envelope.Error))
		default:
			time.Sleep(1 * time.Second)
		}
	}
	return nil, fmt.Errorf("azure DI analysis timed out after 45s")
}

type diEnvelope struct {
	AnalyzeResult struct {
		Content string `json:"content"`
	} `json:"analyzeResult"`
}

func parseResult(raw json.RawMessage) (*Result, error) {
	var env diEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse DI response: %w", err)
	}

	return &Result{
		RawText: env.AnalyzeResult.Content,
		RawJSON: raw,
	}, nil
}
