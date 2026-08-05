package extraction

import (
	"context"

	"github.com/singularityss/oml-quote/internal/models"
)

type RouteFinder interface {
	FindRoute(ctx context.Context, origin, destination string) (*models.Route, error)
}

const confidenceThreshold = 0.5

func ToExtraction(ctx context.Context, intakeID string, s *SpaCyResult, routes RouteFinder) *models.Extraction {
	e := &models.Extraction{
		IntakeID:        intakeID,
		FieldConfidence: map[string]float64{},
		MissingFields:   []string{},
	}

	assign := func(schemaName string, src **string, target **string) {
		e.FieldConfidence[schemaName] = s.Confidence[schemaName]
		if *src == nil || (s.Confidence[schemaName] > 0 && s.Confidence[schemaName] < confidenceThreshold) {
			e.MissingFields = append(e.MissingFields, schemaName)
			return
		}
		val := **src
		*target = &val
	}

	assign("customer_name", &s.CustomerName, &e.CustomerName)
	assign("pickup_location", &s.CargoFrom, &e.PickupLocation)
	assign("destination", &s.Destination, &e.Destination)
	assign("mobilization_point", &s.MobilizationPoint, &e.MobilizationPoint)
	assign("cargo_type", &s.CargoType, &e.CargoType)
	assign("weight_or_volume", &s.WeightVolume, &e.WeightOrVolume)
	assign("delivery_urgency", &s.DeliveryUrgency, &e.DeliveryUrgency)
	assign("special_handling", &s.SpecialHandling, &e.SpecialHandling)

	for _, u := range s.Unresolved {
		found := false
		for _, m := range e.MissingFields {
			if m == u {
				found = true
				break
			}
		}
		if !found {
			e.MissingFields = append(e.MissingFields, u)
		}
	}

	if e.PickupLocation != nil && e.Destination != nil {
		if route, err := routes.FindRoute(ctx, *e.PickupLocation, *e.Destination); err == nil && route != nil {
			e.DistanceKm = &route.DistanceKm
			if e.MobilizationPoint == nil && route.MobilizationPoint != "" {
				mp := route.MobilizationPoint
				e.MobilizationPoint = &mp
			}
		} else {
			e.MissingFields = append(e.MissingFields, "distance_km")
		}
	}

	return e
}
