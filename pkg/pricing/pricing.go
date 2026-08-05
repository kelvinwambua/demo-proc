package pricing

import "math"

type Config struct {
	EPRADieselRateKESPerL       float64
	FuelConsumptionKMPerL       float64
	DepreciationRatePerDayKES   float64
	MaintenanceRatePerKMKES     float64
	InsuranceRatePerDayKES      float64
	DriverRatePerDayKES         float64
	OtherCostsFlatKES           float64
	TargetMarginPct             float64
	AutoApproveMarginFloor      float64
	ManagerReviewMarginFloor    float64
	DirectorEscalationMarginFloor float64
}

type Input struct {
	DistanceKMRoundTrip float64
	AvgKMPerDay         float64
	LoadingDays         float64
}

type Breakdown struct {
	FuelCostKES         float64
	DepreciationCostKES float64
	MaintenanceCostKES  float64
	InsuranceCostKES    float64
	DriverSalaryCostKES float64
	OtherCostsKES       float64
	TotalCostKES        float64
}

type Result struct {
	TravelDays      float64
	DaysTaken       float64
	Breakdown       Breakdown
	QuotedPriceKES  float64
	ActualMarginPct float64
	ApprovalStatus  string
	RejectReason    string
}

func Calculate(cfg Config, in Input) Result {
	travelDays := math.Ceil(in.DistanceKMRoundTrip / in.AvgKMPerDay)
	if travelDays < 1 {
		travelDays = 1
	}
	daysTaken := travelDays + in.LoadingDays

	fuelCost := cfg.EPRADieselRateKESPerL * (in.DistanceKMRoundTrip / cfg.FuelConsumptionKMPerL)
	depreciationCost := cfg.DepreciationRatePerDayKES * daysTaken
	maintenanceCost := cfg.MaintenanceRatePerKMKES * in.DistanceKMRoundTrip
	insuranceCost := cfg.InsuranceRatePerDayKES * daysTaken
	driverSalaryCost := cfg.DriverRatePerDayKES * daysTaken
	otherCosts := cfg.OtherCostsFlatKES

	totalCost := fuelCost + depreciationCost + maintenanceCost + insuranceCost + driverSalaryCost + otherCosts

	var quotedPrice float64
	if cfg.TargetMarginPct >= 1 {
		quotedPrice = totalCost * 2
	} else {
		quotedPrice = totalCost / (1 - cfg.TargetMarginPct)
	}

	var actualMargin float64
	if quotedPrice > 0 {
		actualMargin = (quotedPrice - totalCost) / quotedPrice
	}

	var status string
	var reason string
	switch {
	case actualMargin >= cfg.AutoApproveMarginFloor:
		status = "auto_approved"
	case actualMargin >= cfg.ManagerReviewMarginFloor:
		status = "pending_manager"
	case actualMargin >= cfg.DirectorEscalationMarginFloor:
		status = "pending_director"
	default:
		status = "rejected"
		reason = "margin below director escalation floor"
	}

	return Result{
		TravelDays: travelDays,
		DaysTaken:  daysTaken,
		Breakdown: Breakdown{
			FuelCostKES:         fuelCost,
			DepreciationCostKES: depreciationCost,
			MaintenanceCostKES:  maintenanceCost,
			InsuranceCostKES:    insuranceCost,
			DriverSalaryCostKES: driverSalaryCost,
			OtherCostsKES:       otherCosts,
			TotalCostKES:        totalCost,
		},
		QuotedPriceKES:  quotedPrice,
		ActualMarginPct: actualMargin,
		ApprovalStatus:  status,
		RejectReason:    reason,
	}
}
