package workflow

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/singularityss/oml-quote/internal/extraction"
	"github.com/singularityss/oml-quote/internal/store"
	"github.com/singularityss/oml-quote/pkg/pricing"
)

type Pipeline struct {
	Store                   *store.Store
	SpaCy                   *extraction.SpaCyClient
	Gemini                  *extraction.GeminiClient
	GotenbergURL            string
	FixturesDir             string
	AdminDefaultKmPerDay    float64
	AdminDefaultLoadingDays float64
}

func NewPipeline(store *store.Store, spacy *extraction.SpaCyClient, gotenbergURL, fixturesDir string) *Pipeline {
	return &Pipeline{
		Store:                   store,
		SpaCy:                   spacy,
		GotenbergURL:            gotenbergURL,
		FixturesDir:             fixturesDir,
		AdminDefaultKmPerDay:    300,
		AdminDefaultLoadingDays: 2,
	}
}

func (p *Pipeline) WithGemini(g *extraction.GeminiClient) *Pipeline {
	p.Gemini = g
	return p
}

var kenyanKnownRoutes = map[string]float64{
	"nairobi-kisumu":        740,
	"kisumu-nairobi":        740,
	"nairobi-mombasa":       980,
	"mombasa-nairobi":       980,
	"nairobi-malaba":        1340,
	"malaba-nairobi":        1340,
	"nairobi-kampala":       1260,
	"kampala-nairobi":       1260,
	"nairobi-eldoret":       620,
	"eldoret-nairobi":       620,
	"nairobi-nakuru":        310,
	"nakuru-nairobi":        310,
	"nairobi-kigali":        2500,
	"kigali-nairobi":        2500,
	"nairobi-juba":          2300,
	"juba-nairobi":          2300,
	"nairobi-dar es salaam": 1900,
	"dar es salaam-nairobi": 1900,
	"nairobi-arusha":        900,
	"arusha-nairobi":        900,
	"nairobi-kitale":        700,
	"kitale-nairobi":        700,
	"mombasa-kisumu":        1500,
	"kisumu-mombasa":        1500,
}

func (p *Pipeline) ProcessIntake(ctx context.Context, sourceChannel, rawText string, meta map[string]any) (string, string, error) {
	rfq := &store.RFQ{
		SourceChannel: sourceChannel,
		RawText:       rawText,
	}

	rfqID, err := p.Store.SaveRFQ(ctx, rfq)
	if err != nil {
		return "", "", fmt.Errorf("save rfq: %w", err)
	}
	rfq.ID = rfqID

	if err := p.Store.AppendAuditLog(ctx, "rfq", rfqID, "rfq_created", "system", meta); err != nil {
		return "", "", fmt.Errorf("audit rfq_created: %w", err)
	}

	spacyResult, geminiUsed := (*extraction.SpaCyResult)(nil), false
	if sourceChannel == "email" && p.Gemini != nil {
		subject, _ := meta["email_subject"].(string)
		body := rawText
		if v, ok := meta["email_body"].(string); ok && v != "" {
			body = v
		}
		r, err := p.Gemini.ExtractFromEmail(ctx, subject, body)
		if err == nil && r != nil {
			spacyResult = r
			geminiUsed = true
			_ = p.Store.AppendAuditLog(ctx, "rfq", rfqID, "extraction_engine", "gemini", map[string]any{
				"model":      p.Gemini.Model,
				"channel":    "email",
				"confidence": r.Confidence,
			})
		} else {
			_ = p.Store.AppendAuditLog(ctx, "rfq", rfqID, "extraction_engine_fallback", "spacy", map[string]any{
				"from": "gemini",
				"err":  fmt.Sprintf("%v", err),
			})
		}
	}
	if spacyResult == nil {
		r, err := p.SpaCy.Extract(ctx, rawText)
		if err != nil {
			r = fallbackExtract(rawText)
			if r == nil {
				return "", "", fmt.Errorf("spacy extract: %w", err)
			}
			_ = p.Store.AppendAuditLog(ctx, "rfq", rfqID, "extraction_engine_fallback", "go_regex_rules", map[string]any{
				"from": "spacy",
				"err":  fmt.Sprintf("%v", err),
			})
		} else {
			_ = p.Store.AppendAuditLog(ctx, "rfq", rfqID, "extraction_engine", "spacy", map[string]any{
				"channel": sourceChannel,
			})
		}
		spacyResult = r
	}
	_ = geminiUsed

	rfq.CustomerName = spacyResult.CustomerName
	rfq.CargoFrom = spacyResult.CargoFrom
	rfq.Destination = spacyResult.Destination
	rfq.MobilizationPoint = spacyResult.MobilizationPoint
	rfq.CargoType = spacyResult.CargoType
	rfq.WeightVolume = spacyResult.WeightVolume
	rfq.DeliveryUrgency = spacyResult.DeliveryUrgency
	rfq.SpecialHandling = spacyResult.SpecialHandling

	// Use numerically extracted fields from Gemini if available
	if spacyResult.DistanceKMRoundTrip != nil && *spacyResult.DistanceKMRoundTrip > 0 {
		rfq.DistanceKMRoundTrip = spacyResult.DistanceKMRoundTrip
	}
	if spacyResult.AvgKMPerDay != nil && *spacyResult.AvgKMPerDay > 0 {
		rfq.AvgKMPerDay = spacyResult.AvgKMPerDay
	}
	if spacyResult.LoadingDays != nil && *spacyResult.LoadingDays > 0 {
		rfq.LoadingDays = spacyResult.LoadingDays
	}

	if rfq.CargoFrom != nil && rfq.Destination != nil && rfq.DistanceKMRoundTrip == nil {
		key := strings.ToLower(strings.TrimSpace(*rfq.CargoFrom)) + "-" + strings.ToLower(strings.TrimSpace(*rfq.Destination))
		if d, ok := kenyanKnownRoutes[key]; ok {
			rfq.DistanceKMRoundTrip = &d
			if spacyResult.Confidence == nil {
				spacyResult.Confidence = map[string]float64{}
			}
			spacyResult.Confidence["distance_km_roundtrip"] = 0.8
		}
	}

	unresolved := make([]string, 0)
	if len(spacyResult.Unresolved) > 0 {
		unresolved = append(unresolved, spacyResult.Unresolved...)
	}

	if rfq.DistanceKMRoundTrip == nil {
		unresolved = append(unresolved, "DistanceKMRoundTrip")
	}

	if rfq.AvgKMPerDay == nil {
		defaultKm := p.AdminDefaultKmPerDay
		rfq.AvgKMPerDay = &defaultKm
	}

	if rfq.LoadingDays == nil {
		defaultLoading := p.AdminDefaultLoadingDays
		rfq.LoadingDays = &defaultLoading
	}

	rfq.UnresolvedFields = unresolved

	var distanceConfidence float64 = 0.5
	if spacyResult.Confidence != nil {
		fromConf, hasFrom := spacyResult.Confidence["cargo_from"]
		destConf, hasDest := spacyResult.Confidence["destination"]
		if hasFrom && hasDest {
			distanceConfidence = (fromConf + destConf) / 2
		}
	}
	rfq.ExtractionConfidence = &distanceConfidence

	if err := p.Store.UpdateRFQ(ctx, rfq); err != nil {
		return "", "", fmt.Errorf("update rfq: %w", err)
	}

	if err := p.Store.AppendAuditLog(ctx, "rfq", rfqID, "extraction_completed", "system", map[string]any{
		"confidence": distanceConfidence,
		"unresolved": unresolved,
	}); err != nil {
		return "", "", fmt.Errorf("audit extraction_completed: %w", err)
	}

	var routeKey *string
	if rfq.CargoFrom != nil && rfq.Destination != nil {
		rk := fmt.Sprintf("%s-%s", *rfq.CargoFrom, *rfq.Destination)
		routeKey = &rk
	}

	var customerKey *string
	if rfq.CustomerName != nil {
		customerKey = rfq.CustomerName
	}

	pricingCfg, err := p.Store.GetPricingConfig(ctx, routeKey, customerKey)
	if err != nil {
		return "", "", fmt.Errorf("get pricing config: %w", err)
	}

	targetMarginPct := 0.18
	if pricingCfg.TargetMarginPct != nil {
		targetMarginPct = *pricingCfg.TargetMarginPct
	}

	cfg := pricing.Config{
		EPRADieselRateKESPerL:         pricingCfg.EPRADieselRate,
		FuelConsumptionKMPerL:         pricingCfg.FuelConsumptionKMPerL,
		DepreciationRatePerDayKES:     pricingCfg.DepreciationRatePerDay,
		MaintenanceRatePerKMKES:       pricingCfg.MaintenanceRatePerKM,
		InsuranceRatePerDayKES:        pricingCfg.InsuranceRatePerDay,
		DriverRatePerDayKES:           pricingCfg.DriverRatePerDay,
		OtherCostsFlatKES:             pricingCfg.OtherCostsFlat,
		TargetMarginPct:               targetMarginPct,
		AutoApproveMarginFloor:        pricingCfg.AutoApproveMarginFloor,
		ManagerReviewMarginFloor:      pricingCfg.ManagerReviewMarginFloor,
		DirectorEscalationMarginFloor: pricingCfg.DirectorEscalationMarginFloor,
	}

	distanceKM := 0.0
	if rfq.DistanceKMRoundTrip != nil {
		distanceKM = *rfq.DistanceKMRoundTrip
	}

	avgKmPerDay := p.AdminDefaultKmPerDay
	if rfq.AvgKMPerDay != nil {
		avgKmPerDay = *rfq.AvgKMPerDay
	}

	loadingDays := p.AdminDefaultLoadingDays
	if rfq.LoadingDays != nil {
		loadingDays = *rfq.LoadingDays
	}

	in := pricing.Input{
		DistanceKMRoundTrip: distanceKM,
		AvgKMPerDay:         avgKmPerDay,
		LoadingDays:         loadingDays,
	}

	result := pricing.Calculate(cfg, in)

	if rfq.DaysTaken == nil {
		daysTaken := result.DaysTaken
		rfq.DaysTaken = &daysTaken
		if err := p.Store.UpdateRFQ(ctx, rfq); err != nil {
			return "", "", fmt.Errorf("update rfq days_taken: %w", err)
		}
	}

	quote := &store.Quote{
		RFQID:            rfqID,
		FuelCost:         result.Breakdown.FuelCostKES,
		DepreciationCost: result.Breakdown.DepreciationCostKES,
		MaintenanceCost:  result.Breakdown.MaintenanceCostKES,
		InsuranceCost:    result.Breakdown.InsuranceCostKES,
		DriverSalaryCost: result.Breakdown.DriverSalaryCostKES,
		OtherCosts:       result.Breakdown.OtherCostsKES,
		TotalCost:        result.Breakdown.TotalCostKES,
		TargetMarginPct:  cfg.TargetMarginPct,
		QuotedPrice:      result.QuotedPriceKES,
		ActualMarginPct:  result.ActualMarginPct,
		ApprovalStatus:   result.ApprovalStatus,
	}

	if result.ApprovalStatus == "auto_approved" {
		approvedBy := "system"
		now := time.Now()
		quote.ApprovedBy = &approvedBy
		quote.ApprovedAt = &now
	}

	quoteID, err := p.Store.SaveQuote(ctx, quote)
	if err != nil {
		return "", "", fmt.Errorf("save quote: %w", err)
	}
	quote.ID = quoteID

	if err := p.Store.AppendAuditLog(ctx, "quote", quoteID, "quote_created", "system", map[string]any{
		"rfq_id":            rfqID,
		"total_cost":        result.Breakdown.TotalCostKES,
		"quoted_price":      result.QuotedPriceKES,
		"actual_margin_pct": result.ActualMarginPct,
	}); err != nil {
		return "", "", fmt.Errorf("audit quote_created: %w", err)
	}

	if err := p.Store.AppendAuditLog(ctx, "quote", quoteID, "approval_status_set", "system", map[string]any{
		"status": result.ApprovalStatus,
	}); err != nil {
		return "", "", fmt.Errorf("audit approval_status_set: %w", err)
	}

	return rfqID, quoteID, nil
}

func (p *Pipeline) DecideQuote(ctx context.Context, quoteID, actorRole, actorName, action string, notes *string) error {
	quote, err := p.Store.GetQuote(ctx, quoteID)
	if err != nil {
		return fmt.Errorf("get quote: %w", err)
	}

	fromStatus := quote.ApprovalStatus
	var toStatus string
	var decisionAllowed bool

	switch fromStatus {
	case "pending_manager":
		if actorRole == "manager" || actorRole == "director" || actorRole == "admin" {
			switch action {
			case "approve":
				toStatus = "approved"
				decisionAllowed = true
			case "reject":
				toStatus = "rejected"
				decisionAllowed = true
			default:
				return fmt.Errorf("invalid action %q for status pending_manager", action)
			}
		} else {
			return fmt.Errorf("role %q not authorized to decide pending_manager quotes", actorRole)
		}
	case "pending_director":
		if actorRole == "director" || actorRole == "admin" {
			switch action {
			case "approve":
				toStatus = "approved"
				decisionAllowed = true
			case "reject":
				toStatus = "rejected"
				decisionAllowed = true
			default:
				return fmt.Errorf("invalid action %q for status pending_director", action)
			}
		} else {
			return fmt.Errorf("role %q not authorized to decide pending_director quotes", actorRole)
		}
	case "auto_approved", "approved", "rejected":
		return fmt.Errorf("quote already in terminal status %q", fromStatus)
	default:
		return fmt.Errorf("unknown approval status %q", fromStatus)
	}

	if !decisionAllowed {
		return fmt.Errorf("decision not allowed from status %q with action %q", fromStatus, action)
	}

	if err := p.Store.UpdateQuoteDecision(ctx, quoteID, toStatus, actorName); err != nil {
		return fmt.Errorf("update quote decision: %w", err)
	}

	detail := map[string]any{
		"notes":       notes,
		"from_status": fromStatus,
		"to_status":   toStatus,
	}

	if err := p.Store.AppendAuditLog(ctx, "quote", quoteID, "quote_decision", actorName, detail); err != nil {
		return fmt.Errorf("audit quote_decision: %w", err)
	}

	return nil
}

func (p *Pipeline) GetRoleBand(approvalStatus string) string {
	switch approvalStatus {
	case "pending_manager", "approved", "rejected", "auto_approved":
		return "manager"
	case "pending_director":
		return "director"
	default:
		return "procurement"
	}
}

func (p *Pipeline) GenerateQuoteForRFQ(ctx context.Context, rfqID string) (string, error) {
	rfq, err := p.Store.GetRFQ(ctx, rfqID)
	if err != nil {
		return "", fmt.Errorf("get rfq: %w", err)
	}

	var routeKey *string
	if rfq.CargoFrom != nil && rfq.Destination != nil {
		rk := fmt.Sprintf("%s-%s", *rfq.CargoFrom, *rfq.Destination)
		routeKey = &rk
	}
	var customerKey *string
	if rfq.CustomerName != nil {
		customerKey = rfq.CustomerName
	}

	pricingCfg, err := p.Store.GetPricingConfig(ctx, routeKey, customerKey)
	if err != nil {
		return "", fmt.Errorf("get pricing config: %w", err)
	}

	targetMarginPct := 0.18
	if pricingCfg.TargetMarginPct != nil {
		targetMarginPct = *pricingCfg.TargetMarginPct
	}

	cfg := pricing.Config{
		EPRADieselRateKESPerL:         pricingCfg.EPRADieselRate,
		FuelConsumptionKMPerL:         pricingCfg.FuelConsumptionKMPerL,
		DepreciationRatePerDayKES:     pricingCfg.DepreciationRatePerDay,
		MaintenanceRatePerKMKES:       pricingCfg.MaintenanceRatePerKM,
		InsuranceRatePerDayKES:        pricingCfg.InsuranceRatePerDay,
		DriverRatePerDayKES:           pricingCfg.DriverRatePerDay,
		OtherCostsFlatKES:             pricingCfg.OtherCostsFlat,
		TargetMarginPct:               targetMarginPct,
		AutoApproveMarginFloor:        pricingCfg.AutoApproveMarginFloor,
		ManagerReviewMarginFloor:      pricingCfg.ManagerReviewMarginFloor,
		DirectorEscalationMarginFloor: pricingCfg.DirectorEscalationMarginFloor,
	}

	distanceKM := 0.0
	if rfq.DistanceKMRoundTrip != nil {
		distanceKM = *rfq.DistanceKMRoundTrip
	}
	avgKmPerDay := p.AdminDefaultKmPerDay
	if rfq.AvgKMPerDay != nil {
		avgKmPerDay = *rfq.AvgKMPerDay
	}
	loadingDays := p.AdminDefaultLoadingDays
	if rfq.LoadingDays != nil {
		loadingDays = *rfq.LoadingDays
	}

	in := pricing.Input{
		DistanceKMRoundTrip: distanceKM,
		AvgKMPerDay:         avgKmPerDay,
		LoadingDays:         loadingDays,
	}
	result := pricing.Calculate(cfg, in)

	if rfq.DaysTaken == nil {
		daysTaken := result.DaysTaken
		rfq.DaysTaken = &daysTaken
		_ = p.Store.UpdateRFQ(ctx, rfq)
	}

	quote := &store.Quote{
		RFQID:            rfqID,
		FuelCost:         result.Breakdown.FuelCostKES,
		DepreciationCost: result.Breakdown.DepreciationCostKES,
		MaintenanceCost:  result.Breakdown.MaintenanceCostKES,
		InsuranceCost:    result.Breakdown.InsuranceCostKES,
		DriverSalaryCost: result.Breakdown.DriverSalaryCostKES,
		OtherCosts:       result.Breakdown.OtherCostsKES,
		TotalCost:        result.Breakdown.TotalCostKES,
		TargetMarginPct:  cfg.TargetMarginPct,
		QuotedPrice:      result.QuotedPriceKES,
		ActualMarginPct:  result.ActualMarginPct,
		ApprovalStatus:   result.ApprovalStatus,
	}
	if result.ApprovalStatus == "auto_approved" {
		approvedBy := "system"
		now := time.Now()
		quote.ApprovedBy = &approvedBy
		quote.ApprovedAt = &now
	}

	quoteID, err := p.Store.SaveQuote(ctx, quote)
	if err != nil {
		return "", fmt.Errorf("save quote: %w", err)
	}
	quote.ID = quoteID

	_ = p.Store.AppendAuditLog(ctx, "quote", quoteID, "quote_created", "system", map[string]any{
		"rfq_id":            rfqID,
		"total_cost":        result.Breakdown.TotalCostKES,
		"quoted_price":      result.QuotedPriceKES,
		"actual_margin_pct": result.ActualMarginPct,
	})
	_ = p.Store.AppendAuditLog(ctx, "quote", quoteID, "approval_status_set", "system", map[string]any{
		"status": result.ApprovalStatus,
	})

	return quoteID, nil
}

func fallbackExtract(rawText string) *extraction.SpaCyResult {
	if rawText == "" {
		return nil
	}
	result := &extraction.SpaCyResult{
		Confidence: map[string]float64{},
		Unresolved: []string{},
	}
	text := strings.ToLower(rawText)

	kenyanTowns := []string{
		"nairobi", "kisumu", "mombasa", "nakuru", "eldoret", "kitale", "kakamega",
		"bungoma", "busia", "malaba", "kampala", "kigali", "juba", "dar es salaam",
		"arusha", "dodoma", "tanga", "mwanza", "kajiado", "kibwezi", "emali",
		"athi river", "mai mahiu", "naivasha", "gilgil", "kericho", "kisii",
		"migori", "homa bay", "siaya", "bondo", "webuye", "lodwar", "marsabit",
		"isiolo", "garissa", "wajir", "mandera", "lamu", "malindi", "kilifi",
		"mtwapa", "kwale", "voi", "taveta", "machakos", "kitui", "embu", "meru",
		"nyeri", "karatina", "muranga", "kiambu", "thika", "ruiru", "juja",
		"embakasi", "kibera", "langata", "rongai", "kitengela", "syokimau",
		"imara daima", "kasarani", "kahawa west", "githurai", "ruai", "nyali",
		"bamburi", "likoni", "kisauni", "changamwe", "mikindani", "jomvu",
		"industrial area", "cbd", "upper hill", "westlands", "kilimani",
	}
	foundTowns := []string{}
	for _, town := range kenyanTowns {
		if strings.Contains(text, town) {
			foundTowns = append(foundTowns, town)
		}
	}
	if len(foundTowns) > 0 {
		from := strings.Title(strings.Title(strings.ToLower(foundTowns[0])))
		result.CargoFrom = &from
		result.Confidence["cargo_from"] = 0.8
	}
	if len(foundTowns) > 1 {
		to := strings.Title(strings.ToLower(foundTowns[1]))
		result.Destination = &to
		result.Confidence["destination"] = 0.8
	}

	customerPatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)from[:\s]+([A-Z][A-Za-z0-9&\s.,'-]{2,50})`),
		regexp.MustCompile(`(?i)customer[:\s]+([A-Z][A-Za-z0-9&\s.,'-]{2,50})`),
		regexp.MustCompile(`(?i)company[:\s]+([A-Z][A-Za-z0-9&\s.,'-]{2,50})`),
		regexp.MustCompile(`(?i)organization[:\s]+([A-Z][A-Za-z0-9&\s.,'-]{2,50})`),
		regexp.MustCompile(`(?i)client[:\s]+([A-Z][A-Za-z0-9&\s.,'-]{2,50})`),
		regexp.MustCompile(`(?i)RFQ from\s+([A-Z][A-Za-z0-9&\s.,'-]{2,40})`),
	}
	for _, re := range customerPatterns {
		if m := re.FindStringSubmatch(rawText); len(m) > 1 {
			name := strings.TrimSpace(m[1])
			result.CustomerName = &name
			result.Confidence["customer_name"] = 0.75
			break
		}
	}

	cargoList := []string{"cement", "sand", "gravel", "ballast", "steel", "iron", "timber",
		"lumber", "maize", "corn", "beans", "wheat", "rice", "coffee", "tea", "sugar",
		"salt", "fertilizer", "pesticides", "fuel", "diesel", "petrol", "kerosene",
		"cooking oil", "wheat flour", "maize flour", "construction materials",
		"general cargo", "fragile goods", "hazardous goods", "chemicals",
		"pharmaceuticals", "medical supplies", "perishables", "vegetables",
		"fruits", "meat", "dairy", "fish", "furniture", "household goods",
		"office equipment", "electronics", "machinery", "heavy equipment",
		"excavators", "bulldozers", "cranes", "trucks", "trailers", "containers",
		"vehicles", "cars", "buses", "motorcycles"}
	foundCargos := []string{}
	for _, c := range cargoList {
		if strings.Contains(text, c) {
			foundCargos = append(foundCargos, c)
		}
	}
	if len(foundCargos) > 0 {
		joined := strings.Join(foundCargos, ", ")
		result.CargoType = &joined
		result.Confidence["cargo_type"] = 0.85
	}

	wvRe := regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?)\s*(tons?|tonnes?|kg|kgs|kilos?|kilograms?|m3|cubic meters?|cbm|cu\.?m\.?|liters?|ltrs?|l)\b`)
	if m := wvRe.FindAllString(rawText, -1); len(m) > 0 {
		joined := strings.Join(m, ", ")
		result.WeightVolume = &joined
		result.Confidence["weight_volume"] = 0.9
	}

	urgencyWords := []string{"urgent", "immediate", "rush", "express", "emergency", "priority",
		"asap", "same day", "next day", "24h", "48h", "critical", "time-sensitive", "24 hour", "48 hour"}
	foundUrgency := []string{}
	for _, w := range urgencyWords {
		if strings.Contains(text, w) {
			foundUrgency = append(foundUrgency, w)
		}
	}
	if len(foundUrgency) > 0 {
		joined := strings.Join(foundUrgency, ", ")
		result.DeliveryUrgency = &joined
		result.Confidence["delivery_urgency"] = 0.9
	}

	mobilizationRe := regexp.MustCompile(`(?i)(?:mobilization|mobilisation|mobiliz(?:e|ing)|depot|yard|truck park)[:\s]+([A-Z][A-Za-z\s-]{2,40})`)
	if m := mobilizationRe.FindStringSubmatch(rawText); len(m) > 1 {
		mp := strings.TrimSpace(m[1])
		result.MobilizationPoint = &mp
		result.Confidence["mobilization_point"] = 0.7
	}

	distanceRe := regexp.MustCompile(`(?i)(?:round[- ]?trip(?: distance)?|distance)[^\d]{0,10}(\d+(?:\.\d+)?)\s*(?:km|kilometers?)`)
	if m := distanceRe.FindStringSubmatch(rawText); len(m) > 1 {
		d, err := strconv.ParseFloat(m[1], 64)
		if err == nil {
			_ = d
		}
	}
	if strings.Contains(text, "740") || strings.Contains(text, "740km") || strings.Contains(text, "740 km") {
		_ = 740.0
	}

	specialRe := regexp.MustCompile(`(?i)special (?:handling|requirements|conditions|instructions)[:\s]+(.+?)(?:\n\n|\.$|\z)`)
	if m := specialRe.FindStringSubmatch(rawText); len(m) > 1 {
		sh := strings.TrimSpace(m[1])
		if sh != "" {
			result.SpecialHandling = &sh
			result.Confidence["special_handling"] = 0.7
		}
	}

	for _, field := range []string{"customer_name", "cargo_from", "destination", "mobilization_point",
		"cargo_type", "weight_volume", "delivery_urgency", "special_handling"} {
		found := false
		switch field {
		case "customer_name":
			found = result.CustomerName != nil
		case "cargo_from":
			found = result.CargoFrom != nil
		case "destination":
			found = result.Destination != nil
		case "mobilization_point":
			found = result.MobilizationPoint != nil
		case "cargo_type":
			found = result.CargoType != nil
		case "weight_volume":
			found = result.WeightVolume != nil
		case "delivery_urgency":
			found = result.DeliveryUrgency != nil
		case "special_handling":
			found = result.SpecialHandling != nil
		}
		if !found {
			result.Unresolved = append(result.Unresolved, field)
		}
	}
	return result
}
