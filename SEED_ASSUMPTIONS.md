# OML Quotation Generator — Seed Data Assumptions

> These are DEMO placeholders only. None of these figures are OML's real contracted rates — they were invented on 2026-08-05 to exercise the pricing formula with plausible Kenyan logistics-industry values so the engine, UI, and approval bands run correctly during the live pitch. Substitute with OML's real rates from Finance / Commercial before the demo if provided.

## 1. Global Default Pricing Config (NULL route, NULL customer)

| Column | Seed Value | Unit | Assumption Source / Rationale |
|---|---|---|---|
| epra_diesel_rate | 182.00 | KES / litre | Approximate mid-2025 Kenya EPRA maximum retail pump price for diesel in Nairobi (EPRA publishes monthly). Rounded for a round figure. |
| fuel_consumption_km_per_l | 2.63 | km / litre | Rough fleet average for a 6x4 prime mover pulling a 28-tonne curtain-side trailer on Kenyan highways (literature range: 2.2–3.0 km/l; midpoint rounded). |
| depreciation_rate_per_day | 3,000.00 | KES / day | Straight-line depreciation spread over ~6 operating years for a ~KES 7M truck, divided by ~250 operating days/yr. |
| maintenance_rate_per_km | 18.00 | KES / km | Tires + servicing + repairs per km for heavy truck on Kenyan roads. Industry rule-of-thumb range 12–22 KES/km; mid-high chosen. |
| insurance_rate_per_day | 1,200.00 | KES / day | Comprehensive motor + cargo in-transit insurance, annualised ÷ 250 days. |
| driver_rate_per_day | 2,500.00 | KES / day | Driver daily wage + subsistence, Kenya trucking, non-union, mid-range. Does NOT include crew (turnboy) - for simplicity demo assumes solo driver. |
| other_costs_flat | 4,000.00 | KES / trip | County cess / weighbridge fees / overnight parking / ad-hoc security. Lump-sum average for a Nairobi→Kisumu round trip. |
| target_margin_pct | 18% | (decimal 0.18) | OML stated desired margin from the sample dataset narrative. |
| auto_approve_margin_floor | 20% | (decimal 0.20) | Anything ≥ 20% margin is automatically approved by the system — no human touch needed. |
| manager_review_margin_floor | 12% | (decimal 0.12) | 12–20% band → Manager-level review, can approve or reject. |
| director_escalation_margin_floor | 5% | (decimal 0.05) | 5–12% band → Director-level escalation; below 5% → auto-reject. |

## 2. Route-Specific Override Seed

| Route Key | Override | Value | Rationale |
|---|---|---|---|
| Nairobi–Kisumu | maintenance_rate_per_km | 20.00 KES/km | +11% uplift for the Mau Summit climb section and higher wear on the Londiani–Kericho–Ahero road alignment. Demonstrates route-specific overrides (C2 scoring) without changing the global default. |

## 3. Customer-Specific Agreement Seed

| Customer Key | Override | Notes |
|---|---|---|
| Acme Corp | (no rate overrides in seed; row exists so Admin panel has an example to edit) | Demo customer for showing customer-specific price agreements in the C2 admin UI. Use the Admin panel live on stage to set, e.g., `target_margin_pct = 0.15` and then re-quote to prove the new margin applies. |

## 4. Route Distance / TAT Seed

| Origin | Destination | Mobilization | Round-trip km | Avg km/day | Loading days | Rationale |
|---|---|---|---|---|---|---|
| Nairobi | Kisumu | Mai Mahiu | 740 km | 300 | 2 | Exact figures from the OML provided sample dataset. TAT = ceil(740/300) + 2 = 3 + 2 = 5 days round-trip. |

## 5. Demo Hardcoded Values

- Role logins: the dev server exposes role-switching URLs (`/login?role=procurement|manager|director|admin`) to exercise each approval tier without a full auth system. **Do not ship to production this way.**
- Cryptography key: `ADMIN_SESSION_SECRET` is hashed with SHA-256 to produce a 32-byte AES-256-GCM key for column-level encryption of pricing fields at rest. Replace with a 32+ char random value in production.
- Tender fixture data: tender_portal_sample.json entries are realistic-looking but synthetic RFQ text generated for demo reliability; only the *shape* of the data is guaranteed to match real tender listings, not the content itself.

## 6. Known Gaps / To Be Replaced with Real OML Data

- [ ] All cost rates — OML Finance to confirm actual figures.
- [ ] Fuel consumption figure — OML Fleet to provide real per-model km/l averages.
- [ ] Driver daily rate — OML HR / payroll.
- [ ] Cess / other_flat per route — OML Operations per-route cost card.
- [ ] Margin thresholds — OML Commercial / MD sign-off.
- [ ] Real distances per route — integrate Google Maps Distance Matrix instead of the static `routes` table (left as future work; for demo, static table is zero-risk).
