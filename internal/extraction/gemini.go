package extraction

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"strings"
	"sync"
	"time"
)

type GeminiClient struct {
	Project  string
	Location string
	Model    string

	credsJSON  map[string]any
	saKey      *rsa.PrivateKey
	saEmail    string
	tokenURI   string
	httpClient *http.Client

	mu          sync.Mutex
	accessToken string
	tokenExp    time.Time
}

func NewGeminiClientFromEnv() (*GeminiClient, error) {
	credB64 := strings.TrimSpace(os.Getenv("GOOGLE_CREDENTIALS_B64"))
	project := strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT"))
	location := strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_LOCATION"))
	model := strings.TrimSpace(os.Getenv("GEMINI_MODEL"))
	if model == "" {
		model = "gemini-2.5-flash"
	}
	if credB64 == "" || project == "" || location == "" {
		return nil, fmt.Errorf("GOOGLE_CREDENTIALS_B64, GOOGLE_CLOUD_PROJECT, GOOGLE_CLOUD_LOCATION env vars required for Gemini")
	}
	raw, err := base64.StdEncoding.DecodeString(credB64)
	if err != nil {
		return nil, fmt.Errorf("decode GOOGLE_CREDENTIALS_B64: %w", err)
	}
	var creds map[string]any
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("parse GOOGLE_CREDENTIALS_B64 JSON: %w", err)
	}
	pkPemRaw, ok := creds["private_key"].(string)
	if !ok {
		return nil, fmt.Errorf("credentials missing private_key field")
	}
	saEmail, _ := creds["client_email"].(string)
	tokenURI, _ := creds["token_uri"].(string)
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}
	blk, _ := pem.Decode([]byte(strings.ReplaceAll(pkPemRaw, "\\n", "\n")))
	if blk == nil {
		return nil, fmt.Errorf("PEM decode private_key failed")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		if rsaKey, err2 := x509.ParsePKCS1PrivateKey(blk.Bytes); err2 == nil {
			keyAny = rsaKey
		} else {
			return nil, fmt.Errorf("parse RSA private key (pkcs8): %w (pkcs1 attempt: %v)", err, err2)
		}
	}
	rsaKey, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not RSA (got %T)", keyAny)
	}
	return &GeminiClient{
		Project:    project,
		Location:   location,
		Model:      model,
		credsJSON:  creds,
		saKey:      rsaKey,
		saEmail:    saEmail,
		tokenURI:   tokenURI,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

func b64urlEncode(b []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "=")
}

func (c *GeminiClient) ensureToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken != "" && time.Now().Before(c.tokenExp.Add(-30*time.Second)) {
		return c.accessToken, nil
	}
	now := time.Now().Unix()
	header := map[string]string{
		"alg": "RS256",
		"typ": "JWT",
	}
	headerJSON, _ := json.Marshal(header)
	claims := map[string]any{
		"iss":   c.saEmail,
		"scope": "https://www.googleapis.com/auth/cloud-platform",
		"aud":   c.tokenURI,
		"iat":   now,
		"exp":   now + 3600,
	}
	claimsJSON, _ := json.Marshal(claims)
	toSign := b64urlEncode(headerJSON) + "." + b64urlEncode(claimsJSON)
	hasher := sha256.New()
	hasher.Write([]byte(toSign))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.saKey, 0, hasher.Sum(nil))
	if err != nil {
		return "", fmt.Errorf("sign JWT: %w", err)
	}
	assertion := toSign + "." + b64urlEncode(sig)

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch token: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read token resp: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("token endpoint %d: %s", resp.StatusCode, string(body))
	}
	var tokResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokResp); err != nil {
		return "", fmt.Errorf("parse token resp JSON: %w", err)
	}
	if tokResp.AccessToken == "" {
		return "", fmt.Errorf("empty access_token in token response: %s", string(body))
	}
	c.accessToken = tokResp.AccessToken
	expSec := tokResp.ExpiresIn
	if expSec <= 0 {
		expSec = 3600
	}
	c.tokenExp = time.Now().Add(time.Duration(expSec) * time.Second)
	return c.accessToken, nil
}

type GeminiPromptRequest struct {
	Contents []struct {
		Role  string `json:"role"`
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"contents"`
	GenerationConfig struct {
		ResponseMimeType string  `json:"response_mimeType,omitempty"`
		Temperature      float64 `json:"temperature,omitempty"`
		TopP             float64 `json:"topP,omitempty"`
		MaxOutputTokens  int     `json:"maxOutputTokens,omitempty"`
	} `json:"generationConfig"`
}

type GeminiCandidate struct {
	Content struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"content"`
}

type GeminiResponse struct {
	Candidates []GeminiCandidate `json:"candidates"`
	Error      *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

const emailPromptTemplate = `You are the OML Africa Logistics Ltd (Nairobi-based haulage company) RFQ extraction specialist. Extract ONLY structured fields from the following incoming email (Subject + Body + any quoted / thread). If the email body contains MULTIPLE messages (a thread), treat the LATEST / MOST RECENT / IN-REPLY-TO request (usually the bottom or most-forwarded piece) as the authoritative RFQ text, but scan the entire thread for supplementary data (company names, cargo quantities, dates).

Return a strict SINGLE JSON object with ONLY these keys. Do not wrap in Markdown fences. Do not include any explanation text or comments outside the JSON. If a field is unknown, leave it as null or empty string []:

{
  "customer_name": "Full business name of the company sending the RFQ / requester organisation, e.g. 'Acme Construction Ltd' or 'Kenya Power PLC'",
  "cargo_from": "Origin town / city in Kenya or East Africa (e.g. 'Nairobi', 'Mombasa', 'Mai Mahiu', 'Malaba')",
  "destination": "Destination town / city (e.g. 'Kisumu', 'Kampala', 'Mwanza', 'Juba')",
  "mobilization_point": "Where the truck is mobilised from / demobilised to, often different from origin (e.g. 'Mai Mahiu', 'Embakasi', 'Industrial Area') — null if not mentioned",
  "cargo_type": "Cargo description (e.g. 'cement', 'maize', 'containers 2x20ft', 'construction materials', 'fuel')",
  "weight_volume": "Weight and / or volume with units — e.g. '20 tons', '500 bags 90kg each', '40 m3', '2 x 20ft containers'. Separate multiple values by comma.",
  "delivery_urgency": "Urgency keywords and / or delivery date: e.g. 'urgent, 48h from award', 'delivery by 2026-08-15'.",
  "special_handling": "Any special handling notes — e.g. 'wet-proof tarp required', 'offloading not included', 'border documents provided by client' — null if none.",
  "distance_km_roundtrip": 0.0,
  "avg_km_per_day": 0.0,
  "loading_days": 0.0,
  "confidence": {
    "customer_name": 0.0, "cargo_from": 0.0, "destination": 0.0, "mobilization_point": 0.0,
    "cargo_type": 0.0, "weight_volume": 0.0, "delivery_urgency": 0.0, "special_handling": 0.0,
    "distance_km_roundtrip": 0.0, "avg_km_per_day": 0.0, "loading_days": 0.0
  },
  "unresolved": ["field_name"]
}

Rules:
- Customer name must be the COMPANY / ORGANISATION name (not a person's name). Extract the business from signature blocks, email body statements like 'Customer: X Ltd', 'From [Company]', email domain, or the official organisation. If the body only gives a person's name, search the full email thread / signature block for the company name — 99% of Kenyan RFQ emails sign off with an official company or department.
- If an explicit 'Customer' or 'From' field exists in the email body (e.g., "Customer: Acme Corp Ltd (Purchasing Dept)"), return the clean business entity name (drop parentheticals) e.g. 'Acme Corp Ltd'.
- mobilization_point: Extract the EXACT town / yard / depot name specified. Common OML phrases: 'Truck mobilization from and demobilization to: Mai Mahiu', 'Mobilisation yard: Embakasi', 'Depot: Mai Mahiu OML yard' — return just the place name (drop 'OML yard', 'depot', 'yard' suffixes if the place is a Kenyan town).
- special_handling: return ALL sentences describing handling instructions. Include tarping, offloading rules, border-docs, food-grade, hazardous-liquid flags, escort requirements, etc. Combine with bullet separators. If absolutely none, leave as empty string/null.
- For Kenyan logistics: popular route Nairobi → Kisumu round trip ≈ 740 km, Nairobi→Mombasa ≈ 980 km, Nairobi→Malaba ≈ 1340 km, Nairobi→Kampala ≈ 1260 km round trip, Nairobi→Eldoret≈620km, Nairobi→Nakuru≈310km, Nairobi→Kitale≈700km. If an explicit distance is given in the email, prefer that. If not, but from+destination are known Kenyan cities, fill the round-trip distance above with confidence 0.8. Otherwise leave distance_km_roundtrip 0 / null.
- avg_km_per_day defaults to 300 if email doesn't specify. loading_days defaults to 2 if email doesn't specify.
- For each field, assign a confidence 0.0–1.0 (1.0 when the email states it EXPLICITLY in an itemised list like the OML sample, 0.85 when it's clearly written in the email body, 0.7 when inferred from place-name matching, 0.5 defaulted).
- Add the field name to unresolved ONLY if the email says NOTHING about it AND you cannot confidently infer it from the thread context. avg_km_per_day and loading_days should almost never be in unresolved (default them).
- If the thread is a conversation (RE: chain), the "active request" is the OLDEST inbound message that originally asks for the quote. Treat the most recent delivery-date or quantity update as authoritative overrides.

--- BEGIN EMAIL ---
%s
--- END EMAIL ---`

func (c *GeminiClient) ExtractFromEmail(ctx context.Context, emailSubject, emailBody string) (*SpaCyResult, error) {
	fullEmail := "SUBJECT: " + emailSubject + "\n\nBODY:\n" + emailBody
	prompt := strings.Replace(emailPromptTemplate, "%s", fullEmail, 1)

	token, err := c.ensureToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("gemini auth: %w", err)
	}

	var body GeminiPromptRequest
	body.Contents = make([]struct {
		Role  string `json:"role"`
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	}, 1)
	body.Contents[0].Role = "user"
	body.Contents[0].Parts = []struct {
		Text string `json:"text"`
	}{{Text: prompt}}
	body.GenerationConfig.ResponseMimeType = "application/json"
	body.GenerationConfig.Temperature = 0.1
	body.GenerationConfig.TopP = 0.95
	body.GenerationConfig.MaxOutputTokens = 4096

	payloadBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini request: %w", err)
	}

	endpoint := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent",
		c.Location, c.Project, c.Location, c.Model)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("build gemini req: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call gemini endpoint: %w", err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read gemini resp: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("gemini http %d: %s", resp.StatusCode, string(respBytes))
	}
	var gResp GeminiResponse
	if err := json.Unmarshal(respBytes, &gResp); err != nil {
		return nil, fmt.Errorf("parse gemini resp: %w", err)
	}
	if gResp.Error != nil && gResp.Error.Message != "" {
		return nil, fmt.Errorf("gemini api error %s: %s", gResp.Error.Status, gResp.Error.Message)
	}
	if len(gResp.Candidates) == 0 || len(gResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("gemini returned zero candidates")
	}
	rawJSON := gResp.Candidates[0].Content.Parts[0].Text
	rawJSON = strings.TrimSpace(rawJSON)
	if strings.HasPrefix(rawJSON, "```") {
		rawJSON = strings.TrimPrefix(rawJSON, "```json")
		rawJSON = strings.TrimPrefix(rawJSON, "```")
		if idx := strings.LastIndex(rawJSON, "```"); idx != -1 {
			rawJSON = rawJSON[:idx]
		}
		rawJSON = strings.TrimSpace(rawJSON)
	}

	var parsed struct {
		CustomerName      *string            `json:"customer_name"`
		CargoFrom         *string            `json:"cargo_from"`
		Destination       *string            `json:"destination"`
		MobilizationPoint *string            `json:"mobilization_point"`
		CargoType         *string            `json:"cargo_type"`
		WeightVolume      *string            `json:"weight_volume"`
		DeliveryUrgency   *string            `json:"delivery_urgency"`
		SpecialHandling   *string            `json:"special_handling"`
		DistanceKMRound   any                `json:"distance_km_roundtrip"`
		AvgKmPerDay       any                `json:"avg_km_per_day"`
		LoadingDays       any                `json:"loading_days"`
		Confidence        map[string]float64 `json:"confidence"`
		Unresolved        []string           `json:"unresolved"`
	}
	dec := json.NewDecoder(strings.NewReader(rawJSON))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("parse gemini JSON output: %w; raw=%.200s", err, rawJSON)
	}

	res := &SpaCyResult{
		CustomerName:      parsed.CustomerName,
		CargoFrom:         parsed.CargoFrom,
		Destination:       parsed.Destination,
		MobilizationPoint: parsed.MobilizationPoint,
		CargoType:         parsed.CargoType,
		WeightVolume:      parsed.WeightVolume,
		DeliveryUrgency:   parsed.DeliveryUrgency,
		SpecialHandling:   parsed.SpecialHandling,
		Confidence:        parsed.Confidence,
		Unresolved:        parsed.Unresolved,
	}
	if res.Confidence == nil {
		res.Confidence = map[string]float64{}
	}
	if res.Unresolved == nil {
		res.Unresolved = []string{}
	}
	parseAnyFloat := func(v any) *float64 {
		if v == nil {
			return nil
		}
		var f float64
		var err error
		switch val := v.(type) {
		case json.Number:
			f, err = val.Float64()
		case float64:
			f = val
		default:
			return nil
		}
		if err != nil || f <= 0 {
			return nil
		}
		return &f
	}
	res.DistanceKMRoundTrip = parseAnyFloat(parsed.DistanceKMRound)
	res.AvgKMPerDay = parseAnyFloat(parsed.AvgKmPerDay)
	res.LoadingDays = parseAnyFloat(parsed.LoadingDays)
	return res, nil
}
