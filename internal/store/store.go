package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	omlcrypto "github.com/singularityss/oml-quote/pkg/crypto"
)

type RFQ struct {
	ID                   string
	SourceChannel        string
	RawText              string
	CustomerName         *string
	CargoFrom            *string
	Destination          *string
	MobilizationPoint    *string
	CargoType            *string
	WeightVolume         *string
	DeliveryUrgency      *string
	SpecialHandling      *string
	DistanceKMRoundTrip  *float64
	AvgKMPerDay          *float64
	LoadingDays          *float64
	DaysTaken            *float64
	ExtractionConfidence *float64
	UnresolvedFields     []string
	CreatedAt            time.Time
}

type Quote struct {
	ID               string
	RFQID            string
	FuelCost         float64
	DepreciationCost float64
	MaintenanceCost  float64
	InsuranceCost    float64
	DriverSalaryCost float64
	OtherCosts       float64
	TotalCost        float64
	TargetMarginPct  float64
	QuotedPrice      float64
	ActualMarginPct  float64
	ApprovalStatus   string
	ApprovedBy       *string
	ApprovedAt       *time.Time
	PDFURL           *string
	CreatedAt        time.Time
}

type PricingConfig struct {
	ID                            string
	RouteKey                      *string
	CustomerKey                   *string
	EPRADieselRate                float64
	FuelConsumptionKMPerL         float64
	DepreciationRatePerDay        float64
	MaintenanceRatePerKM          float64
	InsuranceRatePerDay           float64
	DriverRatePerDay              float64
	OtherCostsFlat                float64
	TargetMarginPct               *float64
	AutoApproveMarginFloor        float64
	ManagerReviewMarginFloor      float64
	DirectorEscalationMarginFloor float64
	UpdatedBy                     *string
	UpdatedAt                     time.Time
}

type Store struct {
	Pool *pgxpool.Pool
	Key  []byte
}

func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	key := omlcrypto.DeriveKeyFromEnv()
	return &Store{Pool: pool, Key: key}, nil
}

func (s *Store) Close() {
	s.Pool.Close()
}

// ---------- numeric encryption helpers ----------

func (s *Store) enc(v float64) []byte {
	b, err := omlcrypto.EncryptNumeric(v, s.Key)
	if err != nil {
		panic(fmt.Sprintf("encrypt numeric: %v", err))
	}
	return b
}

func (s *Store) encPtr(v *float64) []byte {
	if v == nil {
		return nil
	}
	return s.enc(*v)
}

func (s *Store) dec(raw []byte) float64 {
	v, err := omlcrypto.DecryptNumeric(raw, s.Key)
	if err != nil {
		panic(fmt.Sprintf("decrypt numeric: %v", err))
	}
	return v
}

func (s *Store) decPtr(raw []byte) *float64 {
	if raw == nil {
		return nil
	}
	v := s.dec(raw)
	return &v
}

// ---------- RFQs ----------

func (s *Store) SaveRFQ(ctx context.Context, r *RFQ) (string, error) {
	if r.UnresolvedFields == nil {
		r.UnresolvedFields = []string{}
	}
	var id string
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO rfqs (
			source_channel, raw_text, customer_name, cargo_from, destination,
			mobilization_point, cargo_type, weight_volume, delivery_urgency,
			special_handling, distance_km_roundtrip, avg_km_per_day, loading_days,
			days_taken, extraction_confidence, unresolved_fields
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		RETURNING id`,
		r.SourceChannel, r.RawText, r.CustomerName, r.CargoFrom, r.Destination,
		r.MobilizationPoint, r.CargoType, r.WeightVolume, r.DeliveryUrgency,
		r.SpecialHandling, r.DistanceKMRoundTrip, r.AvgKMPerDay, r.LoadingDays,
		r.DaysTaken, r.ExtractionConfidence, r.UnresolvedFields,
	).Scan(&id)
	return id, err
}

func (s *Store) GetRFQ(ctx context.Context, id string) (*RFQ, error) {
	var r RFQ
	err := s.Pool.QueryRow(ctx, `
		SELECT id, source_channel, raw_text, customer_name, cargo_from, destination,
		       mobilization_point, cargo_type, weight_volume, delivery_urgency,
		       special_handling, distance_km_roundtrip, avg_km_per_day, loading_days,
		       days_taken, extraction_confidence, unresolved_fields, created_at
		FROM rfqs WHERE id = $1`, id,
	).Scan(&r.ID, &r.SourceChannel, &r.RawText, &r.CustomerName, &r.CargoFrom, &r.Destination,
		&r.MobilizationPoint, &r.CargoType, &r.WeightVolume, &r.DeliveryUrgency,
		&r.SpecialHandling, &r.DistanceKMRoundTrip, &r.AvgKMPerDay, &r.LoadingDays,
		&r.DaysTaken, &r.ExtractionConfidence, &r.UnresolvedFields, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) ListRFQs(ctx context.Context, limit, offset int) ([]RFQ, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, source_channel, raw_text, customer_name, cargo_from, destination,
		       mobilization_point, cargo_type, weight_volume, delivery_urgency,
		       special_handling, distance_km_roundtrip, avg_km_per_day, loading_days,
		       days_taken, extraction_confidence, unresolved_fields, created_at
		FROM rfqs ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RFQ
	for rows.Next() {
		var r RFQ
		if err := rows.Scan(&r.ID, &r.SourceChannel, &r.RawText, &r.CustomerName, &r.CargoFrom, &r.Destination,
			&r.MobilizationPoint, &r.CargoType, &r.WeightVolume, &r.DeliveryUrgency,
			&r.SpecialHandling, &r.DistanceKMRoundTrip, &r.AvgKMPerDay, &r.LoadingDays,
			&r.DaysTaken, &r.ExtractionConfidence, &r.UnresolvedFields, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Store) UpdateRFQ(ctx context.Context, r *RFQ) error {
	if r.UnresolvedFields == nil {
		r.UnresolvedFields = []string{}
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE rfqs SET
			source_channel = $1, raw_text = $2, customer_name = $3, cargo_from = $4,
			destination = $5, mobilization_point = $6, cargo_type = $7, weight_volume = $8,
			delivery_urgency = $9, special_handling = $10, distance_km_roundtrip = $11,
			avg_km_per_day = $12, loading_days = $13, days_taken = $14,
			extraction_confidence = $15, unresolved_fields = $16
		WHERE id = $17`,
		r.SourceChannel, r.RawText, r.CustomerName, r.CargoFrom, r.Destination,
		r.MobilizationPoint, r.CargoType, r.WeightVolume, r.DeliveryUrgency,
		r.SpecialHandling, r.DistanceKMRoundTrip, r.AvgKMPerDay, r.LoadingDays,
		r.DaysTaken, r.ExtractionConfidence, r.UnresolvedFields, r.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("rfq not found")
	}
	return nil
}

// ---------- Quotes ----------

func (s *Store) SaveQuote(ctx context.Context, q *Quote) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO quotes (
			rfq_id, fuel_cost, depreciation_cost, maintenance_cost, insurance_cost,
			driver_salary_cost, other_costs, total_cost, target_margin_pct,
			quoted_price, actual_margin_pct, approval_status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING id`,
		q.RFQID,
		s.enc(q.FuelCost), s.enc(q.DepreciationCost), s.enc(q.MaintenanceCost), s.enc(q.InsuranceCost),
		s.enc(q.DriverSalaryCost), s.enc(q.OtherCosts), s.enc(q.TotalCost), s.enc(q.TargetMarginPct),
		s.enc(q.QuotedPrice), s.enc(q.ActualMarginPct), q.ApprovalStatus,
	).Scan(&id)
	return id, err
}

func (s *Store) GetQuote(ctx context.Context, id string) (*Quote, error) {
	var q Quote
	var fuelB, depB, mainB, insB, drvB, othB, totB, tgtB, quoB, actB []byte
	err := s.Pool.QueryRow(ctx, `
		SELECT id, rfq_id, fuel_cost, depreciation_cost, maintenance_cost, insurance_cost,
		       driver_salary_cost, other_costs, total_cost, target_margin_pct,
		       quoted_price, actual_margin_pct, approval_status, approved_by, approved_at,
		       pdf_url, created_at
		FROM quotes WHERE id = $1`, id,
	).Scan(&q.ID, &q.RFQID, &fuelB, &depB, &mainB, &insB, &drvB, &othB, &totB, &tgtB,
		&quoB, &actB, &q.ApprovalStatus, &q.ApprovedBy, &q.ApprovedAt, &q.PDFURL, &q.CreatedAt)
	if err != nil {
		return nil, err
	}
	q.FuelCost = s.dec(fuelB)
	q.DepreciationCost = s.dec(depB)
	q.MaintenanceCost = s.dec(mainB)
	q.InsuranceCost = s.dec(insB)
	q.DriverSalaryCost = s.dec(drvB)
	q.OtherCosts = s.dec(othB)
	q.TotalCost = s.dec(totB)
	q.TargetMarginPct = s.dec(tgtB)
	q.QuotedPrice = s.dec(quoB)
	q.ActualMarginPct = s.dec(actB)
	return &q, nil
}

func (s *Store) ListQuotesByStatus(ctx context.Context, statuses []string) ([]Quote, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, rfq_id, fuel_cost, depreciation_cost, maintenance_cost, insurance_cost,
		       driver_salary_cost, other_costs, total_cost, target_margin_pct,
		       quoted_price, actual_margin_pct, approval_status, approved_by, approved_at,
		       pdf_url, created_at
		FROM quotes
		WHERE approval_status = ANY($1)
		ORDER BY created_at DESC`, statuses)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Quote
	for rows.Next() {
		var q Quote
		var fuelB, depB, mainB, insB, drvB, othB, totB, tgtB, quoB, actB []byte
		if err := rows.Scan(&q.ID, &q.RFQID, &fuelB, &depB, &mainB, &insB, &drvB, &othB, &totB, &tgtB,
			&quoB, &actB, &q.ApprovalStatus, &q.ApprovedBy, &q.ApprovedAt, &q.PDFURL, &q.CreatedAt); err != nil {
			return nil, err
		}
		q.FuelCost = s.dec(fuelB)
		q.DepreciationCost = s.dec(depB)
		q.MaintenanceCost = s.dec(mainB)
		q.InsuranceCost = s.dec(insB)
		q.DriverSalaryCost = s.dec(drvB)
		q.OtherCosts = s.dec(othB)
		q.TotalCost = s.dec(totB)
		q.TargetMarginPct = s.dec(tgtB)
		q.QuotedPrice = s.dec(quoB)
		q.ActualMarginPct = s.dec(actB)
		out = append(out, q)
	}
	return out, nil
}

func (s *Store) UpdateQuoteDecision(ctx context.Context, quoteID, approvalStatus, approvedBy string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE quotes SET approval_status = $1, approved_by = $2, approved_at = now()
		WHERE id = $3`, approvalStatus, approvedBy, quoteID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("quote not found")
	}
	return nil
}

func (s *Store) SetQuotePDF(ctx context.Context, quoteID, pdfURL string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE quotes SET pdf_url = $1 WHERE id = $2`, pdfURL, quoteID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("quote not found")
	}
	return nil
}

// ---------- Pricing Config ----------

type pricingConfigRow struct {
	RouteKey    *string
	CustomerKey *string
	EPRAB       []byte
	FuelB       []byte
	DepB        []byte
	MaintB      []byte
	InsB        []byte
	DrvB        []byte
	OtherB      []byte
	TargetB     []byte
	AutoB       []byte
	ManagerB    []byte
	DirectorB   []byte
}

func (s *Store) fetchPricingRow(ctx context.Context, routeKey *string, customerKey *string) (*pricingConfigRow, error) {
	var r pricingConfigRow
	var routeSQL, customerSQL any
	if routeKey == nil {
		routeSQL = nil
	} else {
		routeSQL = *routeKey
	}
	if customerKey == nil {
		customerSQL = nil
	} else {
		customerSQL = *customerKey
	}
	err := s.Pool.QueryRow(ctx, `
		SELECT route_key, customer_key, epra_diesel_rate, fuel_consumption_km_per_l,
		       depreciation_rate_per_day, maintenance_rate_per_km, insurance_rate_per_day,
		       driver_rate_per_day, other_costs_flat, target_margin_pct,
		       auto_approve_margin_floor, manager_review_margin_floor, director_escalation_margin_floor
		FROM pricing_config
		WHERE route_key IS NOT DISTINCT FROM $1 AND customer_key IS NOT DISTINCT FROM $2`,
		routeSQL, customerSQL,
	).Scan(&r.RouteKey, &r.CustomerKey, &r.EPRAB, &r.FuelB, &r.DepB, &r.MaintB, &r.InsB,
		&r.DrvB, &r.OtherB, &r.TargetB, &r.AutoB, &r.ManagerB, &r.DirectorB)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

func (s *Store) mergeConfigField(base *float64, raw []byte, exists *bool) {
	if raw == nil {
		return
	}
	v := s.dec(raw)
	*base = v
	if exists != nil {
		*exists = true
	}
}

func (s *Store) GetPricingConfig(ctx context.Context, routeKey *string, customerKey *string) (*PricingConfig, error) {
	orders := [][]any{
		{(*string)(nil), (*string)(nil)},
		{(*string)(nil), customerKey},
		{routeKey, (*string)(nil)},
		{routeKey, customerKey},
	}
	cfg := &PricingConfig{
		RouteKey:    routeKey,
		CustomerKey: customerKey,
	}
	foundAny := false
	for _, ord := range orders {
		rk := ord[0].(*string)
		ck := ord[1].(*string)
		row, err := s.fetchPricingRow(ctx, rk, ck)
		if err != nil {
			return nil, err
		}
		if row == nil {
			continue
		}
		foundAny = true
		s.mergeConfigField(&cfg.EPRADieselRate, row.EPRAB, nil)
		s.mergeConfigField(&cfg.FuelConsumptionKMPerL, row.FuelB, nil)
		s.mergeConfigField(&cfg.DepreciationRatePerDay, row.DepB, nil)
		s.mergeConfigField(&cfg.MaintenanceRatePerKM, row.MaintB, nil)
		s.mergeConfigField(&cfg.InsuranceRatePerDay, row.InsB, nil)
		s.mergeConfigField(&cfg.DriverRatePerDay, row.DrvB, nil)
		s.mergeConfigField(&cfg.OtherCostsFlat, row.OtherB, nil)
		if row.TargetB != nil {
			v := s.dec(row.TargetB)
			cfg.TargetMarginPct = &v
		}
		s.mergeConfigField(&cfg.AutoApproveMarginFloor, row.AutoB, nil)
		s.mergeConfigField(&cfg.ManagerReviewMarginFloor, row.ManagerB, nil)
		s.mergeConfigField(&cfg.DirectorEscalationMarginFloor, row.DirectorB, nil)
	}
	if !foundAny {
		return nil, errors.New("no pricing config found, including global default")
	}
	return cfg, nil
}

func (s *Store) ListPricingConfigs(ctx context.Context) ([]PricingConfig, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, route_key, customer_key, epra_diesel_rate, fuel_consumption_km_per_l,
		       depreciation_rate_per_day, maintenance_rate_per_km, insurance_rate_per_day,
		       driver_rate_per_day, other_costs_flat, target_margin_pct,
		       auto_approve_margin_floor, manager_review_margin_floor, director_escalation_margin_floor,
		       updated_by, updated_at
		FROM pricing_config ORDER BY route_key NULLS FIRST, customer_key NULLS FIRST`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PricingConfig
	for rows.Next() {
		var c PricingConfig
		var eb, fb, db, mb, ib, drvb, ob, tb, ab, mgrb, dirb []byte
		if err := rows.Scan(&c.ID, &c.RouteKey, &c.CustomerKey, &eb, &fb, &db, &mb, &ib,
			&drvb, &ob, &tb, &ab, &mgrb, &dirb, &c.UpdatedBy, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.EPRADieselRate = s.dec(eb)
		c.FuelConsumptionKMPerL = s.dec(fb)
		c.DepreciationRatePerDay = s.dec(db)
		c.MaintenanceRatePerKM = s.dec(mb)
		c.InsuranceRatePerDay = s.dec(ib)
		c.DriverRatePerDay = s.dec(drvb)
		c.OtherCostsFlat = s.dec(ob)
		c.TargetMarginPct = s.decPtr(tb)
		c.AutoApproveMarginFloor = s.dec(ab)
		c.ManagerReviewMarginFloor = s.dec(mgrb)
		c.DirectorEscalationMarginFloor = s.dec(dirb)
		out = append(out, c)
	}
	return out, nil
}

func (s *Store) UpsertPricingConfig(ctx context.Context, c *PricingConfig) error {
	var targetB []byte
	if c.TargetMarginPct != nil {
		targetB = s.encPtr(c.TargetMarginPct)
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO pricing_config (
			route_key, customer_key, epra_diesel_rate, fuel_consumption_km_per_l,
			depreciation_rate_per_day, maintenance_rate_per_km, insurance_rate_per_day,
			driver_rate_per_day, other_costs_flat, target_margin_pct,
			auto_approve_margin_floor, manager_review_margin_floor, director_escalation_margin_floor,
			updated_by, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,now())
		ON CONFLICT (route_key, customer_key) DO UPDATE SET
			epra_diesel_rate = EXCLUDED.epra_diesel_rate,
			fuel_consumption_km_per_l = EXCLUDED.fuel_consumption_km_per_l,
			depreciation_rate_per_day = EXCLUDED.depreciation_rate_per_day,
			maintenance_rate_per_km = EXCLUDED.maintenance_rate_per_km,
			insurance_rate_per_day = EXCLUDED.insurance_rate_per_day,
			driver_rate_per_day = EXCLUDED.driver_rate_per_day,
			other_costs_flat = EXCLUDED.other_costs_flat,
			target_margin_pct = EXCLUDED.target_margin_pct,
			auto_approve_margin_floor = EXCLUDED.auto_approve_margin_floor,
			manager_review_margin_floor = EXCLUDED.manager_review_margin_floor,
			director_escalation_margin_floor = EXCLUDED.director_escalation_margin_floor,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`,
		c.RouteKey, c.CustomerKey,
		s.enc(c.EPRADieselRate), s.enc(c.FuelConsumptionKMPerL), s.enc(c.DepreciationRatePerDay),
		s.enc(c.MaintenanceRatePerKM), s.enc(c.InsuranceRatePerDay), s.enc(c.DriverRatePerDay),
		s.enc(c.OtherCostsFlat), targetB,
		s.enc(c.AutoApproveMarginFloor), s.enc(c.ManagerReviewMarginFloor), s.enc(c.DirectorEscalationMarginFloor),
		c.UpdatedBy)
	return err
}

func (s *Store) DeletePricingConfig(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `
		DELETE FROM pricing_config WHERE id = $1 AND (route_key IS NOT NULL OR customer_key IS NOT NULL)`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("pricing config not found or cannot delete global row")
	}
	return nil
}

func (s *Store) ListQuotesByRFQ(ctx context.Context, rfqID string) ([]Quote, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, rfq_id, fuel_cost, depreciation_cost, maintenance_cost, insurance_cost,
		       driver_salary_cost, other_costs, total_cost, target_margin_pct,
		       quoted_price, actual_margin_pct, approval_status, approved_by, approved_at,
		       pdf_url, created_at
		FROM quotes WHERE rfq_id = $1 ORDER BY created_at DESC`, rfqID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Quote
	for rows.Next() {
		var q Quote
		var fuelB, depB, mainB, insB, drvB, othB, totB, tgtB, quoB, actB []byte
		if err := rows.Scan(&q.ID, &q.RFQID, &fuelB, &depB, &mainB, &insB, &drvB, &othB, &totB, &tgtB,
			&quoB, &actB, &q.ApprovalStatus, &q.ApprovedBy, &q.ApprovedAt, &q.PDFURL, &q.CreatedAt); err != nil {
			return nil, err
		}
		q.FuelCost = s.dec(fuelB)
		q.DepreciationCost = s.dec(depB)
		q.MaintenanceCost = s.dec(mainB)
		q.InsuranceCost = s.dec(insB)
		q.DriverSalaryCost = s.dec(drvB)
		q.OtherCosts = s.dec(othB)
		q.TotalCost = s.dec(totB)
		q.TargetMarginPct = s.dec(tgtB)
		q.QuotedPrice = s.dec(quoB)
		q.ActualMarginPct = s.dec(actB)
		out = append(out, q)
	}
	return out, nil
}

// ---------- Audit Log ----------

// AuditEntry is a single audit log record.
type AuditEntry struct {
	ID         string
	EntityType string
	EntityID   string
	Action     string
	Actor      string
	Detail     json.RawMessage
	CreatedAt  time.Time
}

// ListAuditLog returns the most recent entries for a given entity, newest first.
func (s *Store) ListAuditLog(ctx context.Context, entityType, entityID string, limit int) ([]AuditEntry, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, entity_type, entity_id::text, action, actor, detail, created_at
		FROM audit_log
		WHERE entity_type = $1 AND entity_id = $2::uuid
		ORDER BY created_at DESC
		LIMIT $3`, entityType, entityID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.EntityType, &e.EntityID, &e.Action, &e.Actor, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *Store) AppendAuditLog(ctx context.Context, entityType, entityID, action, actor string, detail any) error {
	var detailJSON []byte
	if detail == nil {
		detailJSON = []byte("{}")
	} else {
		var err error
		detailJSON, err = json.Marshal(detail)
		if err != nil {
			return fmt.Errorf("marshal audit detail: %w", err)
		}
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO audit_log (entity_type, entity_id, action, actor, detail)
		VALUES ($1,$2,$3,$4,$5)`, entityType, entityID, action, actor, detailJSON)
	return err
}
