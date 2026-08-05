package intake

import (
	"encoding/json"
	"io"
	"net/http"
)

type WhatsAppText struct {
	Body string `json:"body"`
}

type WhatsAppMessage struct {
	From string       `json:"from"`
	Text WhatsAppText `json:"text"`
}

type WhatsAppChangeValue struct {
	Messages []WhatsAppMessage `json:"messages"`
}

type WhatsAppChange struct {
	Value WhatsAppChangeValue `json:"value"`
}

type WhatsAppEntry struct {
	Changes []WhatsAppChange `json:"changes"`
}

type WhatsAppWebhookPayload struct {
	Object string          `json:"object"`
	Entry  []WhatsAppEntry `json:"entry"`
}

func ParseWhatsAppWebhook(body []byte) (channel, rawText string, meta map[string]any) {
	channel = "whatsapp"
	meta = map[string]any{}

	var payload WhatsAppWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		rawText = string(body)
		meta["parse_error"] = err.Error()
		return
	}

	if len(payload.Entry) > 0 && len(payload.Entry[0].Changes) > 0 {
		msgs := payload.Entry[0].Changes[0].Value.Messages
		if len(msgs) > 0 {
			rawText = msgs[0].Text.Body
			meta["from"] = msgs[0].From
		}
	}

	return
}

func HandleWhatsAppWebhook(w http.ResponseWriter, r *http.Request) {
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

	channel, rawText, meta := ParseWhatsAppWebhook(body)
	_ = channel
	_ = rawText
	_ = meta

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func GetSampleWhatsAppPayload() string {
	sample := WhatsAppWebhookPayload{
		Object: "whatsapp_business_account",
		Entry: []WhatsAppEntry{
			{
				Changes: []WhatsAppChange{
					{
						Value: WhatsAppChangeValue{
							Messages: []WhatsAppMessage{
								{
									From: "+254700000000",
									Text: WhatsAppText{
										Body: "RFQ — Transport Quotation Request\n\nCustomer: Acme Industries Ltd\nCargo from: Nairobi\nDestination: Kisumu\nTruck mobilization from and demobilization to: Mai Mahiu\nCargo type: Portland cement (bulk)\nWeight/Volume: 20 tons\nDistance (round trip): 740 km\nAverage km covered per day: 300 km\nLoading/Offloading time (days): 2\nDays Taken (TAT): 4\nDelivery urgency: Standard, 96 hours from award\nSpecial handling: Wet-proof tarp required. Offloading not included in scope.\n\nPlease provide a competitive quotation at your earliest convenience.\nRegards,\nJames Mwangi\nProcurement Manager\nAcme Industries Ltd\n+254 712 345 678",
									},
								},
							},
						},
					},
				},
			},
		},
	}
	b, _ := json.MarshalIndent(sample, "", "  ")
	return string(b)
}
