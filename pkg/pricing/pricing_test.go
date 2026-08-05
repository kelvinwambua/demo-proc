package pricing

import "testing"

var sampleKenyaConfig = Config{
	EPRADieselRateKESPerL:           182.0,
	FuelConsumptionKMPerL:           2.63,
	DepreciationRatePerDayKES:       3000.0,
	MaintenanceRatePerKMKES:         18.0,
	InsuranceRatePerDayKES:          1200.0,
	DriverRatePerDayKES:             2500.0,
	OtherCostsFlatKES:               4000.0,
	TargetMarginPct:                 0.18,
	AutoApproveMarginFloor:          0.20,
	ManagerReviewMarginFloor:        0.12,
	DirectorEscalationMarginFloor:   0.05,
}

func TestCalculate_NairobiKisumu_SampleDataset(t *testing.T) {
	in := Input{
		DistanceKMRoundTrip: 740.0,
		AvgKMPerDay:         300.0,
		LoadingDays:         2.0,
	}
	r := Calculate(sampleKenyaConfig, in)

	expectedTravelDays := 3.0
	if r.TravelDays != expectedTravelDays {
		t.Errorf("TravelDays: got %.2f want %.2f", r.TravelDays, expectedTravelDays)
	}
	expectedDaysTaken := 5.0
	if r.DaysTaken != expectedDaysTaken {
		t.Errorf("DaysTaken: got %.2f want %.2f", r.DaysTaken, expectedDaysTaken)
	}
	if r.Breakdown.TotalCostKES <= 0 {
		t.Errorf("TotalCostKES must be positive, got %.2f", r.Breakdown.TotalCostKES)
	}
	if r.QuotedPriceKES <= r.Breakdown.TotalCostKES {
		t.Errorf("QuotedPriceKES %.2f must exceed TotalCostKES %.2f", r.QuotedPriceKES, r.Breakdown.TotalCostKES)
	}
	if r.ApprovalStatus == "" {
		t.Errorf("ApprovalStatus must be set")
	}
	t.Logf("Nairobi→Kisumu sample: Total=%.2f KES, Quote=%.2f KES, Margin=%.4f, Status=%s",
		r.Breakdown.TotalCostKES, r.QuotedPriceKES, r.ActualMarginPct, r.ApprovalStatus)
}

func TestCalculate_ApprovalBands(t *testing.T) {
	baseInput := Input{
		DistanceKMRoundTrip: 740.0,
		AvgKMPerDay:         300.0,
		LoadingDays:         2.0,
	}

	cases := []struct {
		name           string
		targetMargin   float64
		expectedStatus string
	}{
		{
			name:           "auto_approved when margin >= 20%",
			targetMargin:   0.30,
			expectedStatus: "auto_approved",
		},
		{
			name:           "pending_manager when margin between 12-20%",
			targetMargin:   0.15,
			expectedStatus: "pending_manager",
		},
		{
			name:           "pending_director when margin between 5-12%",
			targetMargin:   0.08,
			expectedStatus: "pending_director",
		},
		{
			name:           "rejected when margin below 5%",
			targetMargin:   0.02,
			expectedStatus: "rejected",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := sampleKenyaConfig
			cfg.TargetMarginPct = tc.targetMargin
			r := Calculate(cfg, baseInput)
			if r.ApprovalStatus != tc.expectedStatus {
				t.Errorf("status mismatch: got %s want %s (actual margin=%.4f, target=%.2f)",
					r.ApprovalStatus, tc.expectedStatus, r.ActualMarginPct, tc.targetMargin)
			}
			t.Logf("[%s] target=%.2f actual_margin=%.4f => %s",
				tc.name, tc.targetMargin, r.ActualMarginPct, r.ApprovalStatus)
		})
	}
}

func TestCalculate_FormulaSanity(t *testing.T) {
	cfg := Config{
		EPRADieselRateKESPerL:           100.0,
		FuelConsumptionKMPerL:           2.0,
		DepreciationRatePerDayKES:       1000.0,
		MaintenanceRatePerKMKES:         10.0,
		InsuranceRatePerDayKES:          500.0,
		DriverRatePerDayKES:             800.0,
		OtherCostsFlatKES:               200.0,
		TargetMarginPct:                 0.25,
		AutoApproveMarginFloor:          0.20,
		ManagerReviewMarginFloor:        0.10,
		DirectorEscalationMarginFloor:   0.05,
	}
	in := Input{
		DistanceKMRoundTrip: 100.0,
		AvgKMPerDay:         100.0,
		LoadingDays:         1.0,
	}
	r := Calculate(cfg, in)

	expectedTravelDays := 1.0
	if r.TravelDays != expectedTravelDays {
		t.Errorf("TravelDays: got %.2f want %.2f", r.TravelDays, expectedTravelDays)
	}
	expectedDaysTaken := 2.0
	if r.DaysTaken != expectedDaysTaken {
		t.Errorf("DaysTaken: got %.2f want %.2f", r.DaysTaken, expectedDaysTaken)
	}

	expectedFuel := 100.0 * (100.0 / 2.0)
	if r.Breakdown.FuelCostKES != expectedFuel {
		t.Errorf("FuelCost: got %.2f want %.2f", r.Breakdown.FuelCostKES, expectedFuel)
	}
	expectedDep := 1000.0 * 2.0
	if r.Breakdown.DepreciationCostKES != expectedDep {
		t.Errorf("DepreciationCost: got %.2f want %.2f", r.Breakdown.DepreciationCostKES, expectedDep)
	}
	expectedMaint := 10.0 * 100.0
	if r.Breakdown.MaintenanceCostKES != expectedMaint {
		t.Errorf("MaintenanceCost: got %.2f want %.2f", r.Breakdown.MaintenanceCostKES, expectedMaint)
	}
	expectedIns := 500.0 * 2.0
	if r.Breakdown.InsuranceCostKES != expectedIns {
		t.Errorf("InsuranceCost: got %.2f want %.2f", r.Breakdown.InsuranceCostKES, expectedIns)
	}
	expectedDriver := 800.0 * 2.0
	if r.Breakdown.DriverSalaryCostKES != expectedDriver {
		t.Errorf("DriverSalaryCost: got %.2f want %.2f", r.Breakdown.DriverSalaryCostKES, expectedDriver)
	}
	if r.Breakdown.OtherCostsKES != 200.0 {
		t.Errorf("OtherCosts: got %.2f want 200.0", r.Breakdown.OtherCostsKES)
	}

	expectedTotal := expectedFuel + expectedDep + expectedMaint + expectedIns + expectedDriver + 200.0
	if r.Breakdown.TotalCostKES != expectedTotal {
		t.Errorf("TotalCost: got %.2f want %.2f", r.Breakdown.TotalCostKES, expectedTotal)
	}

	expectedQuote := expectedTotal / (1 - 0.25)
	if r.QuotedPriceKES != expectedQuote {
		t.Errorf("QuotedPrice: got %.2f want %.2f", r.QuotedPriceKES, expectedQuote)
	}

	expectedMargin := (expectedQuote - expectedTotal) / expectedQuote
	if r.ActualMarginPct != expectedMargin {
		t.Errorf("ActualMarginPct: got %.6f want %.6f", r.ActualMarginPct, expectedMargin)
	}
}
