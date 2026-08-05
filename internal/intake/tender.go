package intake

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

type TenderItem struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	ProcurementEntity string `json:"procurement_entity"`
	Deadline         string `json:"deadline"`
	SourceURL        string `json:"source_url"`
}

func LoadTenderFixture(fixturePath string) ([]TenderItem, error) {
	f, err := os.Open(fixturePath)
	if err != nil {
		return nil, fmt.Errorf("open fixture: %w", err)
	}
	defer f.Close()

	b, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read fixture: %w", err)
	}

	var items []TenderItem
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, fmt.Errorf("parse fixture: %w", err)
	}

	return items, nil
}

func (t TenderItem) ToRawText() string {
	return fmt.Sprintf(
		"TENDER: %s\n\nEntity: %s\nDeadline: %s\nSource: %s\n\n%s",
		t.Title,
		t.ProcurementEntity,
		t.Deadline,
		t.SourceURL,
		t.Description,
	)
}

func HandleTenderCheck(w http.ResponseWriter, r *http.Request, fixturePath string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	items, err := LoadTenderFixture(fixturePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("load fixture: %v", err), http.StatusInternalServerError)
		return
	}

	if len(items) == 0 {
		http.Error(w, "fixture is empty", http.StatusInternalServerError)
		return
	}

	item := items[0]
	rawText := item.ToRawText()
	meta := map[string]any{
		"tender_id":          item.ID,
		"title":              item.Title,
		"procurement_entity": item.ProcurementEntity,
		"deadline":           item.Deadline,
		"source_url":         item.SourceURL,
	}

	_ = rawText
	_ = meta

	w.WriteHeader(http.StatusOK)
	resp := map[string]any{
		"status":   "replayed",
		"channel":  "tender",
		"raw_text": rawText,
		"meta":     meta,
	}
	b, _ := json.Marshal(resp)
	_, _ = w.Write(b)
}
