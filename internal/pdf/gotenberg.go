package pdf

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/singularityss/oml-quote/internal/store"
)

func GenerateQuotePDF(ctx context.Context, gotenbergURL string, htmlContent string) ([]byte, error) {
	url := fmt.Sprintf("%s/forms/chromium/convert/html", strings.TrimRight(gotenbergURL, "/"))

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	htmlPart, err := writer.CreateFormFile("files", "index.html")
	if err != nil {
		return nil, fmt.Errorf("create html form file: %w", err)
	}
	if _, err := htmlPart.Write([]byte(htmlContent)); err != nil {
		return nil, fmt.Errorf("write html content: %w", err)
	}

	fields := map[string]string{
		"paperSize":      "A4",
		"marginTop":      "0.5",
		"marginBottom":   "0.5",
		"marginLeft":     "0.5",
		"marginRight":    "0.5",
		"printBackground": "true",
	}

	for key, val := range fields {
		if err := writer.WriteField(key, val); err != nil {
			return nil, fmt.Errorf("write field %s: %w", key, err)
		}
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call gotenberg: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read gotenberg response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gotenberg returned %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

func formatKES(v float64) string {
	return "KES " + strconv.FormatFloat(v, 'f', 2, 64)
}

func ptrString(s *string) string {
	if s == nil {
		return "—"
	}
	return *s
}

func approvalBadge(status string) (string, string) {
	switch status {
	case "approved", "auto_approved":
		return "#16a34a", strings.ToUpper(strings.ReplaceAll(status, "_", " "))
	case "pending_manager":
		return "#ca8a04", "PENDING MANAGER"
	case "pending_director":
		return "#ea580c", "PENDING DIRECTOR"
	case "rejected":
		return "#dc2626", "REJECTED"
	default:
		return "#0891b2", strings.ToUpper(strings.ReplaceAll(status, "_", " "))
	}
}

func RenderQuoteHTML(quote store.Quote, rfq store.RFQ, companyName string) string {
	primary := "#1e40af"
	success := "#16a34a"
	info := "#0891b2"

	badgeColor, badgeLabel := approvalBadge(quote.ApprovalStatus)

	quoteDate := quote.CreatedAt.Format("02 January 2006")
	quoteNumber := fmt.Sprintf("Q-%s", quote.ID[:8])

	origin := ptrString(rfq.CargoFrom)
	dest := ptrString(rfq.Destination)
	customerName := ptrString(rfq.CustomerName)
	cargoType := ptrString(rfq.CargoType)
	weightVolume := ptrString(rfq.WeightVolume)

	targetMarginPct := fmt.Sprintf("%.2f%%", quote.TargetMarginPct*100)
	actualMarginPct := fmt.Sprintf("%.2f%%", quote.ActualMarginPct*100)

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>OML Logistics Quotation - %s</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif;
    color: #1f2937;
    background: #ffffff;
    font-size: 12pt;
    line-height: 1.5;
  }
  .container {
    max-width: 800px;
    margin: 0 auto;
    padding: 20px;
  }
  .header-banner {
    background: %s;
    color: white;
    padding: 24px 28px;
    border-radius: 8px 8px 0 0;
  }
  .header-banner h1 {
    font-size: 22pt;
    font-weight: 800;
    letter-spacing: 1px;
  }
  .header-banner .sub {
    font-size: 10pt;
    opacity: 0.9;
    margin-top: 4px;
    letter-spacing: 3px;
  }
  .quote-meta {
    background: #f8fafc;
    border: 1px solid #e2e8f0;
    border-top: none;
    padding: 16px 28px;
    display: flex;
    justify-content: space-between;
    align-items: center;
    border-radius: 0 0 8px 8px;
  }
  .quote-meta .meta-item {
    display: flex;
    flex-direction: column;
  }
  .quote-meta .label {
    font-size: 8pt;
    color: #64748b;
    text-transform: uppercase;
    letter-spacing: 1px;
    font-weight: 600;
  }
  .quote-meta .value {
    font-size: 13pt;
    font-weight: 700;
    color: #0f172a;
    margin-top: 2px;
  }
  .badge {
    display: inline-block;
    padding: 6px 14px;
    border-radius: 20px;
    font-size: 9pt;
    font-weight: 700;
    color: white;
    letter-spacing: 0.5px;
    background: %s;
  }
  .section {
    margin-top: 24px;
    background: white;
    border: 1px solid #e2e8f0;
    border-radius: 8px;
    overflow: hidden;
  }
  .section-title {
    background: #f1f5f9;
    padding: 12px 20px;
    font-size: 11pt;
    font-weight: 700;
    color: %s;
    text-transform: uppercase;
    letter-spacing: 1px;
    border-bottom: 1px solid #e2e8f0;
  }
  .section-body {
    padding: 18px 20px;
  }
  .customer-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 16px 28px;
  }
  .customer-item {
    display: flex;
    flex-direction: column;
  }
  .customer-item .k {
    font-size: 8pt;
    color: #64748b;
    text-transform: uppercase;
    letter-spacing: 1px;
    font-weight: 600;
  }
  .customer-item .v {
    font-size: 12pt;
    color: #0f172a;
    font-weight: 600;
    margin-top: 3px;
  }
  .route-line {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 11pt;
    color: #475569;
  }
  .route-arrow {
    color: %s;
    font-weight: 800;
  }
  table.cost-table {
    width: 100%%;
    border-collapse: collapse;
    font-size: 11pt;
  }
  table.cost-table th {
    background: #f1f5f9;
    text-align: left;
    padding: 10px 14px;
    font-size: 9pt;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    color: %s;
    font-weight: 700;
    border-bottom: 2px solid #e2e8f0;
  }
  table.cost-table td {
    padding: 10px 14px;
    border-bottom: 1px solid #f1f5f9;
    color: #334155;
  }
  table.cost-table tr:last-child td {
    border-bottom: none;
  }
  table.cost-table .amt {
    text-align: right;
    font-family: 'Courier New', monospace;
    font-weight: 600;
  }
  table.cost-table .total-row td {
    background: #f8fafc;
    font-weight: 800;
    color: #0f172a;
    border-top: 2px solid #e2e8f0;
    border-bottom: none;
    padding-top: 12px;
    padding-bottom: 12px;
  }
  .margin-grid {
    display: grid;
    grid-template-columns: 1fr 1fr 1fr;
    gap: 16px;
  }
  .margin-card {
    padding: 16px;
    border-radius: 8px;
    text-align: center;
  }
  .margin-card.target {
    background: #ecfeff;
    border: 1px solid #a5f3fc;
  }
  .margin-card.price {
    background: #eff6ff;
    border: 1px solid #bfdbfe;
  }
  .margin-card.actual {
    background: #f0fdf4;
    border: 1px solid #bbf7d0;
  }
  .margin-card .mk {
    font-size: 8pt;
    text-transform: uppercase;
    letter-spacing: 1px;
    font-weight: 700;
    color: #64748b;
  }
  .margin-card .mv {
    font-size: 16pt;
    font-weight: 800;
    margin-top: 6px;
  }
  .margin-card.target .mv { color: %s; }
  .margin-card.price .mv { color: %s; }
  .margin-card.actual .mv { color: %s; }
  .footer {
    margin-top: 32px;
    padding-top: 16px;
    border-top: 1px solid #e2e8f0;
    display: flex;
    justify-content: space-between;
    align-items: center;
    font-size: 9pt;
    color: #64748b;
  }
  .footer .co {
    font-weight: 700;
    color: %s;
  }
</style>
</head>
<body>
<div class="container">
  <div class="header-banner">
    <h1>%s QUOTATION</h1>
    <div class="sub">%s</div>
  </div>

  <div class="quote-meta">
    <div class="meta-item">
      <span class="label">Quote Number</span>
      <span class="value">%s</span>
    </div>
    <div class="meta-item" style="text-align:center;">
      <span class="badge">%s</span>
    </div>
    <div class="meta-item" style="text-align:right;">
      <span class="label">Date</span>
      <span class="value">%s</span>
    </div>
  </div>

  <div class="section">
    <div class="section-title">Customer &amp; Shipment Details</div>
    <div class="section-body">
      <div class="customer-grid">
        <div class="customer-item">
          <span class="k">Customer Name</span>
          <span class="v">%s</span>
        </div>
        <div class="customer-item">
          <span class="k">Route</span>
          <span class="v route-line">
            <span>%s</span>
            <span class="route-arrow">→</span>
            <span>%s</span>
          </span>
        </div>
        <div class="customer-item">
          <span class="k">Cargo Type</span>
          <span class="v">%s</span>
        </div>
        <div class="customer-item">
          <span class="k">Weight / Volume</span>
          <span class="v">%s</span>
        </div>
      </div>
    </div>
  </div>

  <div class="section">
    <div class="section-title">Cost Breakdown</div>
    <div class="section-body">
      <table class="cost-table">
        <thead>
          <tr>
            <th>Description</th>
            <th style="text-align:right;">Amount (KES)</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td>Fuel Cost</td>
            <td class="amt">%s</td>
          </tr>
          <tr>
            <td>Depreciation Cost</td>
            <td class="amt">%s</td>
          </tr>
          <tr>
            <td>Maintenance Cost</td>
            <td class="amt">%s</td>
          </tr>
          <tr>
            <td>Insurance Cost</td>
            <td class="amt">%s</td>
          </tr>
          <tr>
            <td>Driver Salary Cost</td>
            <td class="amt">%s</td>
          </tr>
          <tr>
            <td>Other Costs</td>
            <td class="amt">%s</td>
          </tr>
          <tr class="total-row">
            <td>Total Cost</td>
            <td class="amt">%s</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>

  <div class="section">
    <div class="section-title">Margin &amp; Pricing Summary</div>
    <div class="section-body">
      <div class="margin-grid">
        <div class="margin-card target">
          <div class="mk">Target Margin</div>
          <div class="mv">%s</div>
        </div>
        <div class="margin-card price">
          <div class="mk">Quoted Price</div>
          <div class="mv">%s</div>
        </div>
        <div class="margin-card actual">
          <div class="mk">Actual Margin</div>
          <div class="mv">%s</div>
        </div>
      </div>
    </div>
  </div>

  <div class="footer">
    <span class="co">%s Logistics</span>
    <span>Confidential — Generated for internal review</span>
  </div>
</div>
</body>
</html>`,
		quoteNumber,
		primary,
		badgeColor,
		primary,
		primary,
		primary,
		info,
		primary,
		success,
		primary,
		strings.ToUpper(companyName),
		companyName,
		quoteNumber,
		badgeLabel,
		quoteDate,
		customerName,
		origin,
		dest,
		cargoType,
		weightVolume,
		formatKES(quote.FuelCost),
		formatKES(quote.DepreciationCost),
		formatKES(quote.MaintenanceCost),
		formatKES(quote.InsuranceCost),
		formatKES(quote.DriverSalaryCost),
		formatKES(quote.OtherCosts),
		formatKES(quote.TotalCost),
		targetMarginPct,
		formatKES(quote.QuotedPrice),
		actualMarginPct,
		companyName,
	)
}
