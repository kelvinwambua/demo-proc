package intake

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type ResendAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Data        string `json:"data"`
	URL         string `json:"url"`
}

type ResendWebhookPayload struct {
	From        string             `json:"from"`
	To          []string           `json:"to"`
	Subject     string             `json:"subject"`
	Text        string             `json:"text"`
	HTML        string             `json:"html"`
	Attachments []ResendAttachment `json:"attachments"`
}

func ParseResendWebhook(body []byte) (channel, rawText string, meta map[string]any) {
	channel = "email"
	meta = map[string]any{}

	var payload ResendWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		rawText = string(body)
		meta["parse_error"] = err.Error()
		return
	}

	rawText = strings.TrimSpace(payload.Subject + "\n\n" + payload.Text)

	meta["from"] = payload.From
	meta["to"] = payload.To
	meta["subject"] = payload.Subject
	meta["email_subject"] = payload.Subject
	meta["email_body"] = payload.Text
	if len(payload.Attachments) > 0 {
		attMeta := make([]map[string]any, 0, len(payload.Attachments))
		for _, a := range payload.Attachments {
			am := map[string]any{
				"filename":     a.Filename,
				"content_type": a.ContentType,
			}
			if a.Data != "" {
				am["has_data_uri"] = true
			}
			if a.URL != "" {
				am["url"] = a.URL
			}
			attMeta = append(attMeta, am)
		}
		meta["attachments"] = attMeta
	}

	return
}

func HandleResendWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	channel, rawText, meta := ParseResendWebhook(body)
	_ = channel
	_ = rawText
	_ = meta

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
