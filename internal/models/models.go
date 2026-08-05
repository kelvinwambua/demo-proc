package models

import "time"

type Intake struct {
	ID         string
	Channel    string // whatsapp | email | tender
	RawText    string
	RawMeta    map[string]any
	ReceivedAt time.Time
}

type Extraction struct {
	ID                string
	IntakeID          string
	CustomerName      *string
	PickupLocation    *string
	Destination       *string
	MobilizationPoint *string
	CargoType         *string
	WeightOrVolume    *string
	DeliveryUrgency   *string
	SpecialHandling   *string
	DistanceKm        *float64
	FieldConfidence   map[string]float64
	MissingFields     []string
	CreatedAt         time.Time
}

type Route struct {
	ID                string
	Origin            string
	Destination       string
	MobilizationPoint string
	DistanceKm        float64
	AvgKmPerDay       float64
	LoadingDays       float64
}

type CostRates struct {
	DieselPricePerL      float64
	FuelConsumptionPerKm float64
	DepreciationPerDay   float64
	MaintenancePerKm     float64
	InsurancePerDay      float64
	DriverSalaryPerDay   float64
	OtherFlatCost        float64
}

type MarginThresholds struct {
	TargetMarginPct     float64
	AutoApproveAbove    float64
	ManagerReviewAbove  float64
}

type CostBreakdown struct {
	Fuel         float64 `json:"fuel"`
	Depreciation float64 `json:"depreciation"`
	Maintenance  float64 `json:"maintenance"`
	Insurance    float64 `json:"insurance"`
	DriverSalary float64 `json:"driver_salary"`
	Other        float64 `json:"other"`
	TotalCost    float64 `json:"total_cost"`
	TotalDays    float64 `json:"total_days"`
	DistanceKm   float64 `json:"distance_km"`
}

// ApprovalRoute is the routing decision the pricing engine makes based on
// achieved margin vs the configured thresholds (rubric A3 / B3 / B4).
type ApprovalRoute string

const (
	RouteAutoApprove     ApprovalRoute = "auto_approve"
	RouteManagerReview   ApprovalRoute = "pending_manager"
	RouteDirectorReview  ApprovalRoute = "pending_director"
)

type Quote struct {
	ID                 string
	ExtractionID       string
	QuoteNumber        string
	CostBreakdown      CostBreakdown
	TotalCost          float64
	SellingPrice       float64
	AchievedMarginPct  float64
	Route              ApprovalRoute
	Status             string // draft | pending_manager | pending_director | approved | rejected
	DecisionNotes      *string
	DecidedBy          *string
	PDFUrl             *string
	CreatedAt          time.Time
	DecidedAt          *time.Time
}

type CustomerAgreement struct {
	ID           string
	CustomerName string
	DiscountPct  float64
	Notes        string
}
