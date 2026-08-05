package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/singularityss/oml-quote/internal/extraction"
	"github.com/singularityss/oml-quote/internal/intake"
	omlpdf "github.com/singularityss/oml-quote/internal/pdf"
	"github.com/singularityss/oml-quote/internal/store"
	"github.com/singularityss/oml-quote/internal/workflow"
)

type app struct {
	store    *store.Store
	spacy    *extraction.SpaCyClient
	gemini   *extraction.GeminiClient
	azureDI  *extraction.Client
	pipeline *workflow.Pipeline
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Printf("warning: no .env file found: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	endpoint := os.Getenv("AZURE_DOCUMENT_INTELLIGENCE_ENDPOINT")
	key := os.Getenv("AZURE_DOCUMENT_INTELLIGENCE_KEY")
	_ = os.Getenv("RESEND_API_KEY")
	_ = os.Getenv("RESEND_INBOUND_ADDRESS")
	gotenbergURL := envOr("GOTENBERG_URL", "http://localhost:3005")
	spacyURL := envOr("SPACY_SIDECAR_URL", "http://localhost:8001")
	_ = os.Getenv("ADMIN_SESSION_SECRET")
	port := envOr("PORT", "8080")

	log.Println("connecting to database...")
	st, err := store.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("connect store: %v", err)
	}
	defer st.Close()

	spacy := extraction.NewSpaCyClient(spacyURL)
	azureDI := extraction.NewClient(endpoint, key)
	pipeline := workflow.NewPipeline(st, spacy, gotenbergURL, "fixtures")

	var geminiClient *extraction.GeminiClient
	if os.Getenv("GOOGLE_CREDENTIALS_B64") != "" {
		g, err := extraction.NewGeminiClientFromEnv()
		if err != nil {
			log.Printf("warning: gemini unavailable (email extraction will fall back to spacy/regex): %v", err)
		} else {
			geminiClient = g
			pipeline.WithGemini(g)
			log.Printf("Gemini %s wired for email extraction (project=%s, location=%s)", g.Model, g.Project, g.Location)
		}
	}

	app := &app{
		store:    st,
		spacy:    spacy,
		gemini:   geminiClient,
		azureDI:  azureDI,
		pipeline: pipeline,
	}

	mux := http.NewServeMux()

	// ---------- PUBLIC ----------
	mux.HandleFunc("POST /intake/resend", app.handleIntakeResend)
	mux.HandleFunc("POST /intake/whatsapp", app.handleIntakeWhatsApp)
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /login", handleLogin)

	// ---------- PROTECTED: procurement ----------
	mux.Handle("GET /", RequireRole("procurement", "manager", "director", "admin")(http.HandlerFunc(app.handleDashboard)))
	mux.Handle("GET /rfqs/{id}", RequireRole("procurement", "manager", "director", "admin")(http.HandlerFunc(app.handleRFQDetail)))
	mux.Handle("POST /rfqs/{id}/update", RequireRole("procurement", "admin")(http.HandlerFunc(app.handleRFQUpdate)))
	mux.Handle("GET /rfqs/{id}/generate-quote", RequireRole("procurement", "admin")(http.HandlerFunc(app.handleRFQGenerateQuote)))
	mux.Handle("GET /quotes/{id}", RequireRole("procurement", "manager", "director", "admin")(http.HandlerFunc(app.handleQuoteDetail)))
	mux.Handle("POST /quotes/{id}/decide", RequireRole("manager", "director", "admin")(http.HandlerFunc(app.handleQuoteDecide)))
	mux.Handle("POST /quotes/{id}/pdf", RequireRole("procurement", "manager", "director", "admin")(http.HandlerFunc(app.handleQuotePDF)))
	mux.Handle("POST /dev/send-test-whatsapp", RequireRole("procurement", "admin")(http.HandlerFunc(app.handleDevSendTestWhatsApp)))
	mux.Handle("POST /dev/check-tender", RequireRole("procurement", "admin")(http.HandlerFunc(app.handleDevCheckTender)))
	mux.Handle("POST /dev/paste-email", RequireRole("procurement", "admin")(http.HandlerFunc(app.handleDevPasteEmail)))

	// ---------- PROTECTED: manager/director ----------
	mux.Handle("GET /manager", RequireRole("manager", "director", "admin")(http.HandlerFunc(app.handleManagerQueue)))
	mux.Handle("GET /director", RequireRole("director", "admin")(http.HandlerFunc(app.handleDirectorQueue)))

	// ---------- PROTECTED: admin ----------
	mux.Handle("GET /admin", RequireRole("admin")(http.HandlerFunc(app.handleAdmin)))
	mux.Handle("POST /admin/pricing-configs/upsert", RequireRole("admin")(http.HandlerFunc(app.handleAdminUpsertConfig)))
	mux.Handle("POST /admin/pricing-configs/{id}/delete", RequireRole("admin")(http.HandlerFunc(app.handleAdminDeleteConfig)))

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	go func() {
		log.Printf("server starting on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("shutdown: %v", err)
	}
	log.Println("server stopped gracefully")
}

// ============================================================================
// AUTH
// ============================================================================

const sessionCookieName = "oml_session"
const flashCookieName = "oml_flash"

func setFlash(w http.ResponseWriter, msg string) {
	encoded := base64.URLEncoding.EncodeToString([]byte(msg))
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    encoded,
		Path:     "/",
		MaxAge:   60,
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	})
}

func consumeFlash(r *http.Request, w http.ResponseWriter) string {
	c, err := r.Cookie(flashCookieName)
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	})
	if b, err := base64.URLEncoding.DecodeString(c.Value); err == nil {
		return string(b)
	}
	return ""
}

var validRoles = map[string]bool{
	"procurement": true,
	"manager":     true,
	"director":    true,
	"admin":       true,
}

func RequireRole(roles ...string) func(http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, r := range roles {
		allowed[r] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := GetCurrentRole(r)
			if !allowed[role] {
				setFlash(w, fmt.Sprintf("Role %q not authorized for this page. Switch role to: %v.", role, roles))
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func GetCurrentRole(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return "procurement"
	}
	raw, err := base64.StdEncoding.DecodeString(c.Value)
	if err != nil {
		return "procurement"
	}
	role := string(raw)
	if !validRoles[role] {
		return "procurement"
	}
	return role
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	role := r.URL.Query().Get("role")
	if role == "" {
		content := `
<div style="max-width:480px;margin:60px auto">
  <div class="card" style="padding:32px">
    <div style="text-align:center;margin-bottom:28px">
      <div style="font-size:40px;margin-bottom:8px">🚚</div>
      <h1 style="font-size:22px;font-weight:800;color:var(--blue-d);letter-spacing:-.5px">OML Quote System</h1>
      <p class="note mt-2">Select your role to enter the demo. Switch roles anytime from the header.</p>
    </div>
    <div style="display:grid;gap:10px">
      <a href="/login?role=procurement" class="btn btn-outline" style="justify-content:flex-start;padding:14px 16px;border-radius:8px">
        <span style="font-size:18px">🔵</span>
        <div style="text-align:left">
          <div style="font-weight:700;color:var(--blue-d)">Procurement Officer</div>
          <div class="note">Default view — intake, RFQs, quotes, PDF download</div>
        </div>
      </a>
      <a href="/login?role=manager" class="btn btn-outline" style="justify-content:flex-start;padding:14px 16px;border-radius:8px">
        <span style="font-size:18px">🟠</span>
        <div style="text-align:left">
          <div style="font-weight:700;color:#92400e">Operations Manager</div>
          <div class="note">Approve / reject pending_manager quotes (18–30% margin band)</div>
        </div>
      </a>
      <a href="/login?role=director" class="btn btn-outline" style="justify-content:flex-start;padding:14px 16px;border-radius:8px">
        <span style="font-size:18px">🟣</span>
        <div style="text-align:left">
          <div style="font-weight:700;color:#4c1d95">Finance Director</div>
          <div class="note">Director-escalation quotes only (5–18% margin band)</div>
        </div>
      </a>
      <a href="/login?role=admin" class="btn btn-outline" style="justify-content:flex-start;padding:14px 16px;border-radius:8px">
        <span style="font-size:18px">🔴</span>
        <div style="text-align:left">
          <div style="font-weight:700;color:#991b1b">System Admin</div>
          <div class="note">C2 live config: route overrides, margin thresholds, customer agreements</div>
        </div>
      </a>
    </div>
  </div>
</div>`
		renderLayout(w, r, "Login / Switch Role", content)
		return
	}
	if !validRoles[role] {
		role = "procurement"
	}
	cookieVal := base64.StdEncoding.EncodeToString([]byte(role))
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    cookieVal,
		Path:     "/",
		MaxAge:   60 * 60 * 24 * 7,
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	})
	redirectTo := "/"
	if role == "manager" {
		redirectTo = "/manager"
	} else if role == "director" {
		redirectTo = "/director"
	} else if role == "admin" {
		redirectTo = "/admin"
	}
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

// ============================================================================
// PUBLIC HANDLERS
// ============================================================================

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (a *app) handleIntakeResend(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	channel, rawText, meta := intake.ParseResendWebhook(body)
	rfqID, quoteID, err := a.pipeline.ProcessIntake(r.Context(), channel, rawText, meta)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"rfq_id": rfqID, "quote_id": quoteID})
}

func (a *app) handleIntakeWhatsApp(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	channel, rawText, meta := intake.ParseWhatsAppWebhook(body)
	rfqID, quoteID, err := a.pipeline.ProcessIntake(r.Context(), channel, rawText, meta)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"rfq_id": rfqID, "quote_id": quoteID})
}

// ============================================================================
// DEV HANDLERS
// ============================================================================

func (a *app) handleDevSendTestWhatsApp(w http.ResponseWriter, r *http.Request) {
	payload := intake.GetSampleWhatsAppPayload()
	channel, rawText, meta := intake.ParseWhatsAppWebhook([]byte(payload))
	rfqID, quoteID, err := a.pipeline.ProcessIntake(r.Context(), channel, rawText, meta)
	if err != nil {
		setFlash(w, "ER WhatsApp intake failed: "+err.Error())
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	setFlash(w, fmt.Sprintf("✅ WhatsApp RFQ processed → RFQ-%s, Quote-%s", rfqID[:8], quoteID[:8]))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *app) handleDevCheckTender(w http.ResponseWriter, r *http.Request) {
	fixturePath := a.pipeline.FixturesDir + "/tender_portal_sample.json"
	items, err := intake.LoadTenderFixture(fixturePath)
	if err != nil {
		setFlash(w, "ER Load tender fixture: "+err.Error())
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if len(items) == 0 {
		setFlash(w, "❌ Fixture file is empty")
		http.Redirect(w, r, "/", http.StatusSeeOther)
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
	rfqID, quoteID, err := a.pipeline.ProcessIntake(r.Context(), "tender_portal", rawText, meta)
	if err != nil {
		setFlash(w, "ER Tender intake failed: "+err.Error())
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	setFlash(w, fmt.Sprintf("OK Tender portal check processed -> RFQ-%s, Quote-%s", rfqID[:8], quoteID[:8]))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *app) handleDevPasteEmail(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		setFlash(w, "ER Parse form: "+err.Error())
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	subject := strings.TrimSpace(r.FormValue("email_subject"))
	body := strings.TrimSpace(r.FormValue("email_body"))
	if body == "" {
		setFlash(w, "❌ Paste email body first — cannot parse empty email.")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	meta := map[string]any{
		"email_subject":  subject,
		"email_body":     body,
		"submitted_via":  "dashboard_paste_form",
		"resend_inbound": os.Getenv("RESEND_INBOUND_ADDRESS"),
	}
	combined := "SUBJECT: " + subject + "\n\n" + body
	rfqID, quoteID, err := a.pipeline.ProcessIntake(r.Context(), "email", combined, meta)
	if err != nil {
		setFlash(w, "ER Email intake failed: "+err.Error())
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	engine := "Gemini 2.5 Flash (email)"
	if a.gemini == nil {
		engine = "regex fallback (Gemini not configured)"
	}
	setFlash(w, fmt.Sprintf("OK Email processed via %s -> RFQ-%s, Quote-%s", engine, rfqID[:8], quoteID[:8]))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ============================================================================
// VIEW HELPERS
// ============================================================================

type ViewData struct {
	CurrentRole    string
	RoleIsManager  bool
	RoleIsDirector bool
	RoleIsAdmin    bool
	Flashes        []string
	ErrorFlashes   []string
	Content        template.HTML
	Title          string
}

func roleInfo(r *http.Request, w http.ResponseWriter) ViewData {
	role := GetCurrentRole(r)
	vd := ViewData{
		CurrentRole:    role,
		RoleIsManager:  role == "manager",
		RoleIsDirector: role == "director",
		RoleIsAdmin:    role == "admin",
	}
	if f := consumeFlash(r, w); f != "" {
		vd.Flashes = []string{f}
	}
	return vd
}

func roleBadgeClass(role string) string {
	switch role {
	case "procurement":
		return "badge-role-procurement"
	case "manager":
		return "badge-role-manager"
	case "director":
		return "badge-role-director"
	case "admin":
		return "badge-role-admin"
	default:
		return "badge-role-procurement"
	}
}

func roleDisplayName(role string) string {
	switch role {
	case "procurement":
		return "Procurement Officer"
	case "manager":
		return "Operations Manager"
	case "director":
		return "Finance Director"
	case "admin":
		return "System Admin"
	default:
		return role
	}
}

func statusBadgeClass(status string) string {
	switch status {
	case "approved", "auto_approved":
		return "badge-status-approved"
	case "pending_manager":
		return "badge-status-manager"
	case "pending_director":
		return "badge-status-director"
	case "rejected":
		return "badge-status-rejected"
	default:
		return "badge-status-pending"
	}
}

func statusDisplayName(status string) string {
	switch status {
	case "auto_approved":
		return "Auto Approved"
	case "pending_manager":
		return "Pending Manager"
	case "pending_director":
		return "Pending Director"
	case "approved":
		return "Approved"
	case "rejected":
		return "Rejected"
	default:
		return status
	}
}

const css = `
<style>
:root {
  --blue: #1e40af; --blue-d: #1e3a8a; --blue-l: #eff6ff; --blue-border: #bfdbfe;
  --amber: #d97706; --amber-l: #fffbeb; --amber-border: #fde68a;
  --green: #16a34a; --green-l: #f0fdf4; --green-border: #bbf7d0;
  --red: #dc2626; --red-l: #fef2f2; --red-border: #fecaca;
  --purple: #7c3aed; --purple-l: #f5f3ff; --purple-border: #ddd6fe;
  --text: #0f172a; --muted: #64748b; --border: #e2e8f0; --bg: #f1f5f9;
}
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: var(--bg); color: var(--text); font-size: 14px; line-height: 1.5; }
a { color: var(--blue); text-decoration: none; }
a:hover { text-decoration: underline; }

/* ── Header ── */
.header { background: #fff; border-bottom: 2px solid var(--blue); padding: 0 24px; display: flex; align-items: center; justify-content: space-between; height: 56px; gap: 12px; position: sticky; top: 0; z-index: 100; box-shadow: 0 1px 4px rgba(0,0,0,.08); }
.brand { display: flex; align-items: center; gap: 10px; }
.brand-icon { font-size: 22px; }
.brand-text { font-size: 17px; font-weight: 800; color: var(--blue-d); letter-spacing: -.4px; }
.brand-text small { font-weight: 500; color: var(--muted); font-size: 11px; display: block; letter-spacing: .3px; }
.nav { display: flex; align-items: center; gap: 4px; }
.nav a { padding: 6px 12px; border-radius: 6px; color: #475569; font-size: 13px; font-weight: 500; transition: background .15s; }
.nav a:hover { background: var(--blue-l); color: var(--blue); text-decoration: none; }
.nav a.active { background: var(--blue-l); color: var(--blue); font-weight: 600; }
.header-right { display: flex; align-items: center; gap: 10px; }

/* ── Badges ── */
.badge { display: inline-flex; align-items: center; padding: 3px 10px; border-radius: 9999px; font-size: 11px; font-weight: 700; letter-spacing: .3px; white-space: nowrap; }
.badge-role-procurement { background: var(--blue-l); color: var(--blue-d); border: 1px solid var(--blue-border); }
.badge-role-manager { background: var(--amber-l); color: #92400e; border: 1px solid var(--amber-border); }
.badge-role-director { background: var(--purple-l); color: #4c1d95; border: 1px solid var(--purple-border); }
.badge-role-admin { background: var(--red-l); color: #991b1b; border: 1px solid var(--red-border); }
.badge-status-approved { background: var(--green-l); color: #166534; border: 1px solid var(--green-border); }
.badge-status-manager { background: var(--amber-l); color: #92400e; border: 1px solid var(--amber-border); }
.badge-status-director { background: var(--purple-l); color: #4c1d95; border: 1px solid var(--purple-border); }
.badge-status-rejected { background: var(--red-l); color: #991b1b; border: 1px solid var(--red-border); }
.badge-status-pending { background: #f1f5f9; color: #475569; border: 1px solid #cbd5e1; }
.badge-channel { background: #f1f5f9; color: #475569; border: 1px solid #cbd5e1; }
.badge-sim { background: #fef9c3; color: #713f12; border: 1px solid #fde047; font-size: 10px; }
.badge-real { background: var(--green-l); color: #166534; border: 1px solid var(--green-border); font-size: 10px; }

/* ── Layout ── */
.container { max-width: 1240px; margin: 0 auto; padding: 24px 20px; }
.grid-2 { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
.grid-3 { display: grid; grid-template-columns: repeat(3,1fr); gap: 16px; }
.grid-4 { display: grid; grid-template-columns: repeat(4,1fr); gap: 16px; }
@media (max-width: 900px) { .grid-2,.grid-3,.grid-4 { grid-template-columns: 1fr; } }

/* ── Cards ── */
.card { background: #fff; border: 1px solid var(--border); border-radius: 10px; padding: 20px; margin-bottom: 20px; box-shadow: 0 1px 3px rgba(0,0,0,.05); }
.card-header { display: flex; align-items: center; gap: 10px; margin-bottom: 16px; flex-wrap: wrap; }
.card-title { font-size: 15px; font-weight: 700; color: var(--text); letter-spacing: -.2px; flex: 1; }
.card-eyebrow { font-size: 10px; font-weight: 700; color: var(--muted); background: #f1f5f9; padding: 2px 8px; border-radius: 9999px; text-transform: uppercase; letter-spacing: .5px; }
.section-divider { height: 1px; background: var(--border); margin: 16px 0; }

/* ── Stats ── */
.stat { padding: 16px 18px; border-radius: 8px; background: #fff; border: 1px solid var(--border); border-left-width: 4px; }
.stat-blue { border-left-color: var(--blue); }
.stat-amber { border-left-color: var(--amber); }
.stat-purple { border-left-color: var(--purple); }
.stat-green { border-left-color: var(--green); }
.stat-red { border-left-color: var(--red); }
.stat .num { font-size: 28px; font-weight: 800; letter-spacing: -1px; color: var(--text); }
.stat .lbl { font-size: 11px; font-weight: 600; color: var(--muted); text-transform: uppercase; letter-spacing: .5px; margin-top: 4px; }

/* ── Tables ── */
table { width: 100%; border-collapse: collapse; font-size: 13px; }
th { text-align: left; padding: 10px 14px; background: #f8fafc; color: var(--muted); font-size: 10.5px; text-transform: uppercase; letter-spacing: .5px; font-weight: 700; border-bottom: 1px solid var(--border); }
td { padding: 11px 14px; border-bottom: 1px solid #f1f5f9; color: #334155; vertical-align: middle; }
tr:last-child td { border-bottom: none; }
tr:hover td { background: #fafbfc; }
.text-right { text-align: right; }
.font-bold { font-weight: 700; }
.font-mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }

/* ── Buttons ── */
.btn { display: inline-flex; align-items: center; gap: 5px; padding: 8px 16px; border-radius: 6px; font-size: 13px; font-weight: 600; cursor: pointer; border: none; text-decoration: none; line-height: 1.3; transition: background .15s; }
.btn:hover { text-decoration: none; }
.btn-primary { background: var(--blue); color: #fff; }
.btn-primary:hover { background: var(--blue-d); }
.btn-success { background: var(--green); color: #fff; }
.btn-success:hover { background: #15803d; }
.btn-danger { background: var(--red); color: #fff; }
.btn-danger:hover { background: #b91c1c; }
.btn-secondary { background: #475569; color: #fff; }
.btn-secondary:hover { background: #334155; }
.btn-outline { background: #fff; border: 1px solid #cbd5e1; color: #334155; }
.btn-outline:hover { background: #f8fafc; }
.btn-sm { padding: 5px 11px; font-size: 12px; }
.btn-lg { padding: 12px 24px; font-size: 15px; }

/* ── Forms ── */
.field { margin-bottom: 14px; }
.field label { display: block; font-size: 11px; color: var(--muted); margin-bottom: 5px; font-weight: 700; text-transform: uppercase; letter-spacing: .3px; }
.field input, .field textarea, .field select,
.form-control { width: 100%; padding: 8px 11px; border: 1px solid #cbd5e1; border-radius: 6px; font-size: 13px; font-family: inherit; background: #fff; color: var(--text); }
.field input:focus, .field textarea:focus, .form-control:focus { outline: none; border-color: var(--blue); box-shadow: 0 0 0 3px rgba(37,99,235,.12); }
.form-mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; resize: vertical; line-height: 1.6; }

/* ── Extracted field states ── */
.field-confident { background: #f0fdf4; border-color: #86efac !important; }
.field-medium { background: #fffbeb; border-color: #fde68a !important; }
.field-unresolved { background: #fef2f2 !important; border-color: #fca5a5 !important; color: #991b1b; }
.field-unresolved::placeholder { color: #f87171; }
.conf-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 4px; vertical-align: middle; }
.conf-high { background: var(--green); }
.conf-mid { background: var(--amber); }
.conf-low { background: var(--red); }
.field-label-row { display: flex; align-items: center; justify-content: space-between; margin-bottom: 5px; }
.field-label-text { font-size: 11px; color: var(--muted); font-weight: 700; text-transform: uppercase; letter-spacing: .3px; }
.conf-label { font-size: 10px; font-weight: 600; }
.conf-label-high { color: var(--green); }
.conf-label-mid { color: var(--amber); }
.conf-label-low { color: var(--red); }

/* ── Alert / info boxes ── */
.alert { padding: 12px 16px; border-radius: 8px; font-size: 13px; margin-bottom: 16px; border: 1px solid; }
.alert-info { background: var(--blue-l); color: #1e40af; border-color: var(--blue-border); }
.alert-warning { background: var(--amber-l); color: #92400e; border-color: var(--amber-border); }
.alert-danger { background: var(--red-l); color: #991b1b; border-color: var(--red-border); }
.alert-success { background: var(--green-l); color: #166534; border-color: var(--green-border); }

/* ── Flash messages ── */
.flash-group { margin: 0 0 16px; }
.flash { padding: 10px 16px; border-radius: 7px; margin-bottom: 8px; font-size: 13px; font-weight: 500; border: 1px solid; }
.flash-success { background: var(--green-l); color: #166534; border-color: var(--green-border); }
.flash-error { background: var(--red-l); color: #991b1b; border-color: var(--red-border); }
.flash-info { background: var(--blue-l); color: var(--blue); border-color: var(--blue-border); }

/* ── Utilities ── */
.muted { color: var(--muted); font-size: 12px; }
.note { font-size: 12px; color: var(--muted); line-height: 1.6; }
.actions { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
form.inline { display: inline; }
.ml-auto { margin-left: auto; }
.mt-2 { margin-top: 8px; }
.mt-3 { margin-top: 12px; }
.mt-4 { margin-top: 16px; }
.mb-2 { margin-bottom: 8px; }
.mb-3 { margin-bottom: 12px; }
.mb-4 { margin-bottom: 16px; }
.link { color: var(--blue); font-weight: 600; }
.link:hover { text-decoration: underline; }
code.inline { background: #f1f5f9; padding: 2px 6px; border-radius: 4px; font-size: 11px; font-family: ui-monospace, Menlo, monospace; }
.empty { padding: 28px 16px; text-align: center; color: var(--muted); font-size: 13px; background: #fafbfc; border-radius: 6px; }
.divider { height: 1px; background: var(--border); margin: 16px 0; }

/* ── Dropdown ── */
.dropdown { position: relative; display: inline-block; }
.dropdown-content { display: none; position: absolute; right: 0; top: calc(100% + 4px); background: #fff; border: 1px solid var(--border); border-radius: 8px; min-width: 210px; box-shadow: 0 8px 24px rgba(0,0,0,.12); z-index: 200; overflow: hidden; }
.dropdown:hover .dropdown-content { display: block; }
.dropdown-content a { display: flex; align-items: center; gap: 8px; padding: 10px 14px; color: #334155; font-size: 13px; font-weight: 500; border-bottom: 1px solid #f1f5f9; }
.dropdown-content a:last-child { border-bottom: none; }
.dropdown-content a:hover { background: var(--blue-l); color: var(--blue); text-decoration: none; }

/* ── Queue header ── */
.queue-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 14px; }
.queue-head h2 { margin: 0; font-size: 15px; font-weight: 700; }

/* ── Price display ── */
.price-hero { text-align: center; padding: 24px; background: var(--blue-l); border-radius: 10px; border: 2px solid var(--blue-border); margin-bottom: 16px; }
.price-hero .ph-label { font-size: 11px; font-weight: 700; color: var(--blue); text-transform: uppercase; letter-spacing: .5px; }
.price-hero .ph-amount { font-size: 40px; font-weight: 800; color: var(--blue-d); letter-spacing: -1.5px; margin: 4px 0; }
.price-hero .ph-margin { font-size: 14px; font-weight: 600; color: var(--green); }
.cost-total-row td { font-weight: 800; font-size: 14px; background: #f8fafc; border-top: 2px solid var(--border); }

/* ── Intake triggers ── */
.intake-grid { display: grid; grid-template-columns: repeat(3,1fr); gap: 12px; }
@media (max-width: 900px) { .intake-grid { grid-template-columns: 1fr; } }
.intake-card { border: 1px solid var(--border); border-radius: 8px; padding: 14px 16px; background: #fafbfc; }
.intake-card .ic-head { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.intake-card .ic-icon { font-size: 18px; }
.intake-card .ic-label { font-weight: 700; font-size: 13px; }
.intake-card .ic-note { font-size: 11px; color: var(--muted); line-height: 1.5; margin-top: 6px; }

/* ── Audit log ── */
.audit-entry { display: flex; gap: 10px; padding: 8px 0; border-bottom: 1px solid #f1f5f9; font-size: 12px; }
.audit-entry:last-child { border-bottom: none; }
.audit-entry .ae-time { color: var(--muted); white-space: nowrap; min-width: 110px; }
.audit-entry .ae-action { font-weight: 600; color: var(--text); }
.audit-entry .ae-actor { color: var(--muted); }

/* ── Card headings & legacy ── */
.card h2 { font-size: 15px; font-weight: 700; color: var(--text); margin-bottom: 14px; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.card h3 { font-size: 13px; font-weight: 700; color: #334155; margin-bottom: 10px; }
.eyebrow { font-size: 10px; font-weight: 700; color: var(--muted); background: #f1f5f9; padding: 2px 8px; border-radius: 9999px; text-transform: uppercase; letter-spacing: .5px; }
.count { font-size: 11px; font-weight: 700; padding: 3px 10px; border-radius: 9999px; background: #e2e8f0; color: #334155; }
.channel { background: #f1f5f9 !important; color: #475569 !important; border: 1px solid #e2e8f0 !important; }
.info-box { background: var(--blue-l); border: 1px solid var(--blue-border); color: var(--blue); padding: 10px 14px; border-radius: 6px; font-size: 13px; }
.unresolved { background: var(--red-l) !important; color: #991b1b; border: 1px solid var(--red-border) !important; }
</style>`

func renderLayout(w io.Writer, r *http.Request, title string, contentHTML string, flashes ...string) {
	var rw http.ResponseWriter
	if resp, ok := w.(http.ResponseWriter); ok {
		rw = resp
	}
	vd := roleInfo(r, rw)
	vd.Title = title
	vd.Content = template.HTML(contentHTML)
	for _, f := range flashes {
		vd.Flashes = append(vd.Flashes, f)
	}

	flashHTML := ""
	if len(vd.Flashes) > 0 {
		flashHTML += `<div class="flash-group">`
		for _, f := range vd.Flashes {
			cls := "flash-info"
			trimmed := f
			if len(f) > 2 {
				prefix := f[:2]
				switch prefix {
				case "OK":
					cls = "flash-success"
					trimmed = strings.TrimSpace(f[2:])
				case "ER":
					cls = "flash-error"
					trimmed = strings.TrimSpace(f[2:])
				case "IN":
					cls = "flash-info"
					trimmed = strings.TrimSpace(f[2:])
				}
			}
			flashHTML += `<div class="flash ` + cls + `">` + template.HTMLEscapeString(trimmed) + `</div>`
		}
		flashHTML += `</div>`
	}

	navActive := func(p string) string {
		if r.URL.Path == p {
			return "active"
		}
		return ""
	}

	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s — OML Quote</title>
%s
</head>
<body>
<header class="header">
  <div class="brand">
    <span class="brand-icon">🚚</span>
    <div class="brand-text">OML Haulage <small>AI Quote System</small></div>
  </div>
  <nav class="nav">
    <a href="/" class="%s">Dashboard</a>
    <a href="/manager" class="%s">Manager Queue</a>
    <a href="/director" class="%s">Director Queue</a>
    <a href="/admin" class="%s">Admin Config</a>
  </nav>
  <div class="header-right">
    <span class="badge %s">%s</span>
    <div class="dropdown">
      <button class="btn btn-outline btn-sm">Switch Role ▾</button>
      <div class="dropdown-content">
        <a href="/login?role=procurement">🔵 Procurement Officer</a>
        <a href="/login?role=manager">🟠 Operations Manager</a>
        <a href="/login?role=director">🟣 Finance Director</a>
        <a href="/login?role=admin">🔴 System Admin</a>
      </div>
    </div>
  </div>
</header>
<main class="container">
%s
%s
</main>
</body>
</html>`,
		title,
		css,
		navActive("/"),
		navActive("/manager"),
		navActive("/director"),
		navActive("/admin"),
		roleBadgeClass(vd.CurrentRole),
		roleDisplayName(vd.CurrentRole),
		flashHTML,
		vd.Content,
	)
}

func fmtMoney(v float64) string {
	return fmt.Sprintf("KES %.2f", v)
}

func fmtPct(v float64) string {
	return fmt.Sprintf("%.2f%%", v*100)
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ptrStrHTML(s *string, unresolved bool) string {
	if unresolved || s == nil || *s == "" {
		return `<span class="unresolved"></span>`
	}
	return template.HTMLEscapeString(*s)
}

func ptrFloatHTML(v *float64, unresolved bool) string {
	if unresolved || v == nil {
		return `<span class="unresolved"></span>`
	}
	return fmt.Sprintf("%.2f", *v)
}

// ============================================================================
// DASHBOARD
// ============================================================================

func (a *app) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rfqs, err := a.store.ListRFQs(ctx, 50, 0)
	if err != nil {
		rfqs = []store.RFQ{}
	}

	quotes, err := a.store.ListQuotesByStatus(ctx, []string{"auto_approved", "approved", "pending_manager", "pending_director", "rejected"})
	if err != nil {
		quotes = []store.Quote{}
	}

	quotesByStatus := map[string][]store.Quote{
		"approved":         {},
		"pending_manager":  {},
		"pending_director": {},
		"rejected":         {},
	}
	for _, q := range quotes {
		status := q.ApprovalStatus
		if status == "auto_approved" {
			status = "approved"
		}
		quotesByStatus[status] = append(quotesByStatus[status], q)
	}

	mustRowHTML := func(parts ...string) string { return strings.Join(parts, "") }
	_ = mustRowHTML

	rfqRows := ""
	for _, rfq := range rfqs {
		cust := ptrStr(rfq.CustomerName)
		if cust == "" {
			cust = `<span class="muted">—</span>`
		} else {
			cust = template.HTMLEscapeString(cust)
		}
		from := ptrStr(rfq.CargoFrom)
		if from == "" {
			from = "—"
		}
		to := ptrStr(rfq.Destination)
		if to == "" {
			to = "—"
		}
		ch := template.HTMLEscapeString(rfq.SourceChannel)
		route := template.HTMLEscapeString(from) + ` &rarr; ` + template.HTMLEscapeString(to)
		unresolvedCount := fmt.Sprintf("%d fields", len(rfq.UnresolvedFields))
		created := rfq.CreatedAt.Format("02 Jan 15:04")
		rfqIDShort := rfq.ID[:8]
		rfqRows += `<tr>` +
			`<td><a class="link" href="/rfqs/` + rfq.ID + `">RFQ-` + rfqIDShort + `</a></td>` +
			`<td><span class="badge badge-channel">` + ch + `</span></td>` +
			`<td>` + cust + `</td>` +
			`<td>` + route + `</td>` +
			`<td>` + unresolvedCount + `</td>` +
			`<td class="muted">` + created + `</td>` +
			`</tr>`
	}
	if rfqRows == "" {
		rfqRows = `<tr><td colspan="6" class="empty">No RFQs yet. Trigger a test intake below.</td></tr>`
	}

	quoteTable := func(status string) string {
		qs := quotesByStatus[status]
		if len(qs) == 0 {
			return `<p class="empty">None</p>`
		}
		rows := ""
		for _, q := range qs {
			rows += `<tr>` +
				`<td><a class="link" href="/quotes/` + q.ID + `">Q-` + q.ID[:8] + `</a></td>` +
				`<td>RFQ-` + q.RFQID[:8] + `</td>` +
				`<td style="text-align:right;font-weight:600">` + fmtMoney(q.QuotedPrice) + `</td>` +
				`<td style="text-align:right">` + fmtPct(q.ActualMarginPct) + `</td>` +
				`</tr>`
		}
		return `<table>` +
			`<thead><tr><th>Quote</th><th>RFQ</th><th style="text-align:right">Price</th><th style="text-align:right">Margin</th></tr></thead>` +
			`<tbody>` + rows + `</tbody></table>`
	}

	totalRFQs := fmt.Sprintf("%d", len(rfqs))
	pendingMgrCount := fmt.Sprintf("%d", len(quotesByStatus["pending_manager"]))
	pendingDirCount := fmt.Sprintf("%d", len(quotesByStatus["pending_director"]))
	approvedCount := fmt.Sprintf("%d", len(quotesByStatus["approved"]))
	resendInbox := template.HTMLEscapeString(envOr("RESEND_INBOUND_ADDRESS", "(not configured)"))

	engineLabel := `<span class="badge badge-role-director" style="font-size:10px">Gemini 2.5 Flash</span>`
	if a.gemini == nil {
		engineLabel = `<span class="badge badge-channel" style="font-size:10px">spaCy + regex fallback</span>`
	}

	pendingMgrHead := `<div class="queue-head"><h2 style="margin:0">Pending Manager Approval</h2><span class="badge badge-status-manager">` + pendingMgrCount + `</span></div>`
	pendingDirHead := `<div class="queue-head"><h2 style="margin:0">Pending Director Approval</h2><span class="badge badge-status-director">` + pendingDirCount + `</span></div>`
	approvedHead := `<div class="queue-head"><h2 style="margin:0">Approved Quotes</h2><span class="badge badge-status-approved">` + approvedCount + `</span></div>`
	rejectedCount := fmt.Sprintf("%d", len(quotesByStatus["rejected"]))
	rejectedHead := `<div class="queue-head"><h2 style="margin:0">Rejected Quotes</h2><span class="badge badge-status-rejected">` + rejectedCount + `</span></div>`

	content := ""

	content += `<div class="grid-4 mb-4">` +
		`<div class="stat stat-blue"><div class="num">` + totalRFQs + `</div><div class="lbl">Total RFQs</div></div>` +
		`<div class="stat stat-amber"><div class="num">` + pendingMgrCount + `</div><div class="lbl">Pending Manager</div></div>` +
		`<div class="stat stat-purple"><div class="num">` + pendingDirCount + `</div><div class="lbl">Pending Director</div></div>` +
		`<div class="stat stat-green"><div class="num">` + approvedCount + `</div><div class="lbl">Approved</div></div>` +
		`</div>`

	content += `<div class="card">
  <div class="card-header">
    <span class="card-title">Intake Channels</span>
    <span class="card-eyebrow">Demo Tools</span>
  </div>
  <div class="intake-grid">
    <div class="intake-card">
      <div class="ic-head">
        <span class="ic-icon">📱</span>
        <span class="ic-label">WhatsApp</span>
        <span class="badge badge-sim">Simulated</span>
      </div>
      <form method="post" action="/dev/send-test-whatsapp">
        <button class="btn btn-primary" type="submit" style="width:100%">Send Test WhatsApp RFQ</button>
      </form>
      <p class="ic-note">Fires the OML sample RFQ (Nairobi→Kisumu, 20t cement) through the WhatsApp Business API payload shape. Transport simulated; extraction pipeline is identical to production.</p>
    </div>
    <div class="intake-card">
      <div class="ic-head">
        <span class="ic-icon">📋</span>
        <span class="ic-label">Tender Portal</span>
        <span class="badge badge-sim">Cached</span>
      </div>
      <form method="post" action="/dev/check-tender">
        <button class="btn btn-secondary" type="submit" style="width:100%">Replay Tender Fixture</button>
      </form>
      <p class="ic-note">Replays real tender data scraped ahead of time. Exact same pipeline as live scrape — zero risk of external site being down during demo.</p>
    </div>
    <div class="intake-card">
      <div class="ic-head">
        <span class="ic-icon">📧</span>
        <span class="ic-label">Email (Resend)</span>
        <span class="badge badge-real">Live</span>
      </div>
      <div class="alert-info alert" style="font-size:11px;padding:8px 10px;margin-bottom:8px">Inbound: <code class="inline">` + resendInbox + `</code></div>
      <p class="ic-note">Real Resend inbound webhook on /intake/resend. Paste an email body below to trigger the pipeline offline.</p>
    </div>
  </div>
</div>`

	content += `<div class="card">
  <div class="card-header">
    <span class="card-title">📧 Paste Email Body — Extract &amp; Price</span>
    ` + engineLabel + `
  </div>
  <form method="post" action="/dev/paste-email">
    <div class="grid-2">
      <div class="field">
        <label>Email Subject</label>
        <input class="form-control" type="text" name="email_subject" placeholder="RFQ — Nairobi to Kisumu 20 tons cement, 48h delivery">
      </div>
      <div></div>
    </div>
    <div class="field">
      <label>Full Email Body (From, To, headers, body, signatures — paste everything)</label>
      <textarea class="form-control form-mono" name="email_body" rows="9" placeholder="From: procurement@client.co.ke&#10;To: quotes@omlafricalogistics.com&#10;Subject: RFQ — ...&#10;&#10;Dear OML,&#10;Please quote for transport of 20 tons cement from Nairobi to Kisumu..."></textarea>
    </div>
    <button class="btn btn-primary btn-lg" type="submit">Extract → Price → Route for Approval</button>
  </form>
</div>`

	content += `<div class="card">` +
		`<div class="card-header"><span class="card-title">Recent RFQs</span><span class="card-eyebrow">Last 50</span></div>` +
		`<table>` +
		`<thead><tr><th>RFQ</th><th>Channel</th><th>Customer</th><th>Route</th><th>Fields</th><th>Created</th></tr></thead>` +
		`<tbody>` + rfqRows + `</tbody>` +
		`</table>` +
		`</div>`

	content += `<div class="grid-2">` +
		`<div class="card">` + pendingMgrHead + quoteTable("pending_manager") + `</div>` +
		`<div class="card">` + pendingDirHead + quoteTable("pending_director") + `</div>` +
		`</div>`

	content += `<div class="grid-2">` +
		`<div class="card">` + approvedHead + quoteTable("approved") + `</div>` +
		`<div class="card">` + rejectedHead + quoteTable("rejected") + `</div>` +
		`</div>`

	renderLayout(w, r, "Dashboard", content)
}

// ============================================================================
// RFQ DETAIL
// ============================================================================

func (a *app) handleRFQDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	rfq, err := a.store.GetRFQ(ctx, id)
	if err != nil {
		http.Error(w, "RFQ not found: "+err.Error(), http.StatusNotFound)
		return
	}

	quotes, _ := a.store.ListQuotesByRFQ(ctx, id)
	unresolved := map[string]bool{}
	for _, u := range rfq.UnresolvedFields {
		unresolved[u] = true
	}

	quoteLinks := ""
	for _, q := range quotes {
		quoteLinks += fmt.Sprintf(`<a href="/quotes/%s" class="btn btn-outline btn-sm">Q-%s · <span class="badge %s">%s</span> · %s</a>`,
			q.ID, q.ID[:8], statusBadgeClass(q.ApprovalStatus), statusDisplayName(q.ApprovalStatus), fmtMoney(q.QuotedPrice))
	}
	if quoteLinks == "" {
		quoteLinks = `<p class="muted">No quotes yet.</p>`
	}

	field := func(label, key string, htmlVal string) string {
		isUnres := unresolved[key]
		var indicatorClass, indicatorText string
		if isUnres {
			indicatorClass = "conf-label conf-label-low"
			indicatorText = "⚠ Cannot determine"
		} else {
			indicatorClass = "conf-label conf-label-high"
			indicatorText = "✓ Extracted"
		}
		return fmt.Sprintf(`<div class="field">
      <div class="field-label-row">
        <span class="field-label-text">%s</span>
        <span class="%s">%s</span>
      </div>
      %s
    </div>`, label, indicatorClass, indicatorText, htmlVal)
	}

	inputClass := func(key string) string {
		if unresolved[key] {
			return "form-control field-unresolved"
		}
		return "form-control"
	}

	content := fmt.Sprintf(`
<div class="card">
  <div class="card-header">
    <span class="card-title">RFQ-%s Details</span>
    <span class="badge badge-channel">%s</span>
    <a href="/rfqs/%s/generate-quote" class="btn btn-primary btn-sm ml-auto">Re-generate Quote →</a>
  </div>

  <div class="mb-4">
    <h3>Linked Quotes</h3>
    <div class="actions">%s</div>
  </div>

  <div class="divider"></div>

  <form method="post" action="/rfqs/%s/update">
    <h3>Extracted Fields — AI Extraction Results</h3>
    <div class="grid-2">
      %s
      %s
      %s
      %s
      %s
      %s
      %s
      %s
    </div>

    <div class="divider"></div>
    <h3>Quantitative Fields (editable overrides)</h3>
    <div class="grid-4">
      %s
      %s
      %s
      %s
    </div>

    <div class="divider"></div>
    <div class="field">
      <label>Raw Text (original)</label>
      <textarea rows="6" readonly class="form-mono" style="background:#f8fafc">%s</textarea>
    </div>

    <div class="actions">
      <button type="submit" class="btn btn-primary">Save Changes</button>
      <a href="/" class="btn btn-outline">Back</a>
    </div>
  </form>
</div>
`,
		id[:8], rfq.SourceChannel, id,
		quoteLinks,
		id,
		field("Customer Name", "customer_name", `<input class="`+inputClass("customer_name")+`" type="text" name="customer_name" value="`+template.HTMLEscapeString(ptrStr(rfq.CustomerName))+`">`),
		field("Origin (Cargo From)", "cargo_from", `<input class="`+inputClass("cargo_from")+`" type="text" name="cargo_from" value="`+template.HTMLEscapeString(ptrStr(rfq.CargoFrom))+`">`),
		field("Destination", "destination", `<input class="`+inputClass("destination")+`" type="text" name="destination" value="`+template.HTMLEscapeString(ptrStr(rfq.Destination))+`">`),
		field("Mobilization Point", "mobilization_point", `<input class="`+inputClass("mobilization_point")+`" type="text" name="mobilization_point" value="`+template.HTMLEscapeString(ptrStr(rfq.MobilizationPoint))+`">`),
		field("Cargo Type", "cargo_type", `<input class="`+inputClass("cargo_type")+`" type="text" name="cargo_type" value="`+template.HTMLEscapeString(ptrStr(rfq.CargoType))+`">`),
		field("Weight / Volume", "weight_volume", `<input class="`+inputClass("weight_volume")+`" type="text" name="weight_volume" value="`+template.HTMLEscapeString(ptrStr(rfq.WeightVolume))+`">`),
		field("Delivery Urgency", "delivery_urgency", `<input class="`+inputClass("delivery_urgency")+`" type="text" name="delivery_urgency" value="`+template.HTMLEscapeString(ptrStr(rfq.DeliveryUrgency))+`">`),
		field("Special Handling", "special_handling", `<input class="`+inputClass("special_handling")+`" type="text" name="special_handling" value="`+template.HTMLEscapeString(ptrStr(rfq.SpecialHandling))+`">`),
		field("Distance (km, round trip)", "distance_km", `<input class="`+inputClass("distance_km")+`" type="number" step="any" name="distance_km" value="`+ptrFloatStr(rfq.DistanceKMRoundTrip, unresolved["distance_km"])+`">`),
		field("Avg KM / Day", "avg_km_per_day", `<input class="`+inputClass("avg_km_per_day")+`" type="number" step="any" name="avg_km_per_day" value="`+ptrFloatStr(rfq.AvgKMPerDay, false)+`">`),
		field("Loading Days", "loading_days", `<input class="`+inputClass("loading_days")+`" type="number" step="any" name="loading_days" value="`+ptrFloatStr(rfq.LoadingDays, false)+`">`),
		field("Days Taken", "days_taken", `<input class="`+inputClass("days_taken")+`" type="number" step="any" name="days_taken" value="`+ptrFloatStr(rfq.DaysTaken, false)+`">`),
		template.HTMLEscapeString(rfq.RawText),
	)

	renderLayout(w, r, "RFQ Details", content)
}

func (a *app) handleRFQUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	rfq, err := a.store.GetRFQ(ctx, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "parse form", http.StatusBadRequest)
		return
	}

	optionalStr := func(key string, current **string) {
		v := r.FormValue(key)
		if v != "" {
			*current = &v
		} else {
			*current = nil
		}
	}
	optionalFloat := func(key string, current **float64) {
		v := r.FormValue(key)
		if v == "" {
			return
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return
		}
		*current = &f
	}

	optionalStr("customer_name", &rfq.CustomerName)
	optionalStr("cargo_from", &rfq.CargoFrom)
	optionalStr("destination", &rfq.Destination)
	optionalStr("mobilization_point", &rfq.MobilizationPoint)
	optionalStr("cargo_type", &rfq.CargoType)
	optionalStr("weight_volume", &rfq.WeightVolume)
	optionalStr("delivery_urgency", &rfq.DeliveryUrgency)
	optionalStr("special_handling", &rfq.SpecialHandling)
	optionalFloat("distance_km", &rfq.DistanceKMRoundTrip)
	optionalFloat("avg_km_per_day", &rfq.AvgKMPerDay)
	optionalFloat("loading_days", &rfq.LoadingDays)
	optionalFloat("days_taken", &rfq.DaysTaken)

	rfq.UnresolvedFields = recalcUnresolved(rfq)

	if err := a.store.UpdateRFQ(ctx, rfq); err != nil {
		setFlash(w, "ER RFQ update failed: "+err.Error())
		http.Redirect(w, r, "/rfqs/"+id, http.StatusSeeOther)
		return
	}
	setFlash(w, "OK RFQ fields saved successfully")
	http.Redirect(w, r, "/rfqs/"+id, http.StatusSeeOther)
}

func (a *app) handleRFQGenerateQuote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	_, err := a.store.GetRFQ(ctx, id)
	if err != nil {
		setFlash(w, "ER RFQ not found: "+err.Error())
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	quoteID, err := a.pipeline.GenerateQuoteForRFQ(ctx, id)
	if err != nil {
		setFlash(w, "ER Quote generation failed: "+err.Error())
		http.Redirect(w, r, "/rfqs/"+id, http.StatusSeeOther)
		return
	}
	setFlash(w, "OK Quote generated successfully")
	http.Redirect(w, r, "/quotes/"+quoteID, http.StatusSeeOther)
}

func recalcUnresolved(rfq *store.RFQ) []string {
	out := []string{}
	if rfq.CustomerName == nil || *rfq.CustomerName == "" {
		out = append(out, "customer_name")
	}
	if rfq.CargoFrom == nil || *rfq.CargoFrom == "" {
		out = append(out, "cargo_from")
	}
	if rfq.Destination == nil || *rfq.Destination == "" {
		out = append(out, "destination")
	}
	if rfq.MobilizationPoint == nil || *rfq.MobilizationPoint == "" {
		out = append(out, "mobilization_point")
	}
	if rfq.CargoType == nil || *rfq.CargoType == "" {
		out = append(out, "cargo_type")
	}
	if rfq.WeightVolume == nil || *rfq.WeightVolume == "" {
		out = append(out, "weight_volume")
	}
	if rfq.DeliveryUrgency == nil || *rfq.DeliveryUrgency == "" {
		out = append(out, "delivery_urgency")
	}
	if rfq.SpecialHandling == nil || *rfq.SpecialHandling == "" {
		out = append(out, "special_handling")
	}
	if rfq.DistanceKMRoundTrip == nil {
		out = append(out, "distance_km")
	}
	if rfq.AvgKMPerDay == nil {
		out = append(out, "avg_km_per_day")
	}
	if rfq.LoadingDays == nil {
		out = append(out, "loading_days")
	}
	if rfq.DaysTaken == nil {
		out = append(out, "days_taken")
	}
	return out
}

func ptrFloatStr(v *float64, unresolved bool) string {
	if unresolved || v == nil {
		return ""
	}
	return fmt.Sprintf("%.2f", *v)
}

// ============================================================================
// QUOTE DETAIL
// ============================================================================

func (a *app) handleQuoteDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	q, err := a.store.GetQuote(ctx, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	rfq, _ := a.store.GetRFQ(ctx, q.RFQID)
	role := GetCurrentRole(r)

	canDecide := false
	if (q.ApprovalStatus == "pending_manager") && (role == "manager" || role == "director" || role == "admin") {
		canDecide = true
	}
	if (q.ApprovalStatus == "pending_director") && (role == "director" || role == "admin") {
		canDecide = true
	}

	decideForm := ""
	if canDecide {
		decideForm = `
<div class="card" style="border-color:var(--amber-border);border-width:2px">
  <div class="card-header">
    <span class="card-title">✍ Approval Decision Required</span>
    <span class="badge badge-status-manager">Awaiting Review</span>
  </div>
  <form method="post" action="/quotes/` + id + `/decide">
    <div class="field">
      <label>Decision Notes (optional — will be logged to audit trail)</label>
      <textarea class="form-control" name="notes" rows="3" placeholder="Reason for approval or rejection..."></textarea>
    </div>
    <div class="actions">
      <button type="submit" name="action" value="approve" class="btn btn-success btn-lg">✓ Approve Quote</button>
      <button type="submit" name="action" value="reject" class="btn btn-danger btn-lg">✕ Reject Quote</button>
    </div>
  </form>
</div>`
	}

	from := "—"
	to := "—"
	customer := "—"
	if rfq != nil {
		if rfq.CargoFrom != nil {
			from = *rfq.CargoFrom
		}
		if rfq.Destination != nil {
			to = *rfq.Destination
		}
		if rfq.CustomerName != nil {
			customer = *rfq.CustomerName
		}
	}

	qidShort := id[:8]
	statusBadge := `<span class="badge ml-auto ` + statusBadgeClass(q.ApprovalStatus) + `">` + statusDisplayName(q.ApprovalStatus) + `</span>`
	customerH := template.HTMLEscapeString(customer)
	routeH := template.HTMLEscapeString(from) + ` &rarr; ` + template.HTMLEscapeString(to)
	rfqLink := `<a class="link" href="/rfqs/` + q.RFQID + `">RFQ-` + q.RFQID[:8] + `</a>`
	pdfBtn := `<form class="inline" method="post" action="/quotes/` + id + `/pdf" target="_blank"><button class="btn btn-primary" type="submit">Download PDF</button></form>`
	viewRFQBtn := `<a href="/rfqs/` + q.RFQID + `" class="btn btn-outline">View RFQ</a>`

	created := q.CreatedAt.Format("02 Jan 2006 15:04")
	approvedHTML := approvedBy(q.ApprovedBy)
	pdfHTML := pdfLink(q.PDFURL, id)

	content := ""
	content += `<div class="card">` +
		`<div class="card-header">` +
		`<span class="card-title">Quote Q-` + qidShort + `</span>` +
		statusBadge +
		`</div>` +
		`<div class="grid-3 mb-4">` +
		`<div><div class="muted">Customer</div><div style="font-weight:600;margin-top:2px">` + customerH + `</div></div>` +
		`<div><div class="muted">Route</div><div style="font-weight:600;margin-top:2px">` + routeH + `</div></div>` +
		`<div><div class="muted">Source RFQ</div>` + rfqLink + `</div>` +
		`</div>` +
		`<div class="actions">` + pdfBtn + viewRFQBtn + `</div>` +
		`</div>`

	content += `<div class="grid-2">` +
		`<div class="card">` +
		`<div class="price-hero">` +
		`<div class="ph-label">Quoted Price (incl. ` + fmtPct(q.TargetMarginPct) + ` target margin)</div>` +
		`<div class="ph-amount">` + fmtMoney(q.QuotedPrice) + `</div>` +
		`<div class="ph-margin">Achieved margin: ` + fmtPct(q.ActualMarginPct) + `</div>` +
		`</div>` +
		`<table>` +
		`<tr><th>Cost Component</th><th class="text-right">Amount (KES)</th></tr>` +
		`<tr><td>Fuel</td><td class="text-right font-mono">` + fmtMoney(q.FuelCost) + `</td></tr>` +
		`<tr><td>Depreciation</td><td class="text-right font-mono">` + fmtMoney(q.DepreciationCost) + `</td></tr>` +
		`<tr><td>Maintenance</td><td class="text-right font-mono">` + fmtMoney(q.MaintenanceCost) + `</td></tr>` +
		`<tr><td>Insurance</td><td class="text-right font-mono">` + fmtMoney(q.InsuranceCost) + `</td></tr>` +
		`<tr><td>Driver Salary</td><td class="text-right font-mono">` + fmtMoney(q.DriverSalaryCost) + `</td></tr>` +
		`<tr><td>Other Costs (county cess, security)</td><td class="text-right font-mono">` + fmtMoney(q.OtherCosts) + `</td></tr>` +
		`<tr class="cost-total-row"><td>Total Cost</td><td class="text-right font-mono">` + fmtMoney(q.TotalCost) + `</td></tr>` +
		`</table>` +
		`</div>` +
		`<div>` +
		`<div class="card">` +
		`<div class="card-header"><span class="card-title">Approval Status</span></div>` +
		`<div class="grid-2">` +
		`<div><div class="muted">Status</div><div style="margin-top:4px">` + statusBadge + `</div></div>` +
		`<div><div class="muted">Approved By</div><div style="font-weight:600;margin-top:4px">` + approvedHTML + `</div></div>` +
		`<div><div class="muted">Created</div><div style="margin-top:4px">` + created + `</div></div>` +
		`<div><div class="muted">PDF Quotation</div><div style="margin-top:4px">` + pdfHTML + `</div></div>` +
		`</div>` +
		`</div>` +
		decideForm +
		`</div>` +
		`</div>`

	// Audit trail
	auditEntries, _ := a.store.ListAuditLog(ctx, "quote", id, 10)
	auditHTML := ""
	for _, e := range auditEntries {
		auditHTML += `<div class="audit-entry">` +
			`<span class="ae-time">` + e.CreatedAt.Format("02 Jan 15:04") + `</span>` +
			`<span class="ae-action">` + template.HTMLEscapeString(e.Action) + `</span>` +
			`<span class="ae-actor">— ` + template.HTMLEscapeString(e.Actor) + `</span>` +
			`</div>`
	}
	if auditHTML == "" {
		auditHTML = `<p class="muted">No audit entries yet.</p>`
	}
	content += `<div class="card">` +
		`<div class="card-header"><span class="card-title">Audit Trail</span><span class="card-eyebrow">Last 10 events</span></div>` +
		auditHTML +
		`</div>`

	renderLayout(w, r, "Quote "+qidShort, content)
}

func approvedBy(s *string) string {
	if s == nil || *s == "" {
		return `<span class="muted">Not yet</span>`
	}
	return *s
}

func pdfLink(s *string, id string) string {
	if s != nil && *s != "" {
		return fmt.Sprintf(`<a href="%s">Download</a>`, *s)
	}
	return fmt.Sprintf(`<span class="muted">Not generated</span> · <form class="inline" method="post" action="/quotes/%s/pdf"><button class="btn btn-outline btn-sm" type="submit">Generate</button></form>`, id)
}

func (a *app) handleQuoteDecide(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		setFlash(w, "ER Failed to parse form")
		http.Redirect(w, r, "/quotes/"+id, http.StatusSeeOther)
		return
	}
	action := r.FormValue("action")
	notes := r.FormValue("notes")
	var notesPtr *string
	if notes != "" {
		notesPtr = &notes
	}
	role := GetCurrentRole(r)
	actor := role + " (demo)"
	if err := a.pipeline.DecideQuote(r.Context(), id, role, actor, action, notesPtr); err != nil {
		setFlash(w, "ER Decision failed: "+err.Error())
		http.Redirect(w, r, "/quotes/"+id, http.StatusSeeOther)
		return
	}
	verb := "rejected"
	if action == "approve" {
		verb = "approved"
	}
	setFlash(w, "OK Quote "+verb+" successfully")
	http.Redirect(w, r, "/quotes/"+id, http.StatusSeeOther)
}

func (a *app) handleQuotePDF(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	q, err := a.store.GetQuote(ctx, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	rfq, _ := a.store.GetRFQ(ctx, q.RFQID)

	htmlContent := omlpdf.RenderQuoteHTML(*q, *rfq, "OML")
	pdfBytes, err := omlpdf.GenerateQuotePDF(ctx, a.pipeline.GotenbergURL, htmlContent)
	if err != nil {
		http.Error(w, "pdf gen: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="QUOTE-%s.pdf"`, id))
	w.Header().Set("Content-Length", strconv.Itoa(len(pdfBytes)))
	w.Write(pdfBytes)
}

// ============================================================================
// QUEUES (MANAGER / DIRECTOR)
// ============================================================================

func (a *app) handleManagerQueue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	quotes, err := a.store.ListQuotesByStatus(ctx, []string{"pending_manager"})
	if err != nil {
		quotes = []store.Quote{}
	}
	renderQueue(w, r, "Manager Queue — Pending Approvals", quotes, "pending_manager")
}

func (a *app) handleDirectorQueue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	quotes, err := a.store.ListQuotesByStatus(ctx, []string{"pending_director"})
	if err != nil {
		quotes = []store.Quote{}
	}
	renderQueue(w, r, "Director Queue — Escalated Approvals", quotes, "pending_director")
}

func renderQueue(w http.ResponseWriter, r *http.Request, title string, quotes []store.Quote, status string) {
	countBadge := `<span class="badge ` + statusBadgeClass(status) + `">` + fmt.Sprintf("%d items", len(quotes)) + `</span>`

	var rows string
	for _, q := range quotes {
		marginClass := ""
		if q.ActualMarginPct < 0.12 {
			marginClass = `style="color:var(--red);font-weight:700"`
		} else if q.ActualMarginPct < 0.20 {
			marginClass = `style="color:var(--amber);font-weight:700"`
		} else {
			marginClass = `style="color:var(--green);font-weight:700"`
		}
		rows += `<tr>` +
			`<td><a class="link" href="/quotes/` + q.ID + `">Q-` + q.ID[:8] + `</a></td>` +
			`<td class="muted">RFQ-` + q.RFQID[:8] + `</td>` +
			`<td class="text-right font-mono">` + fmtMoney(q.TotalCost) + `</td>` +
			`<td class="text-right font-mono font-bold">` + fmtMoney(q.QuotedPrice) + `</td>` +
			`<td class="text-right" ` + marginClass + `>` + fmtPct(q.ActualMarginPct) + `</td>` +
			`<td class="muted">` + q.CreatedAt.Format("02 Jan 15:04") + `</td>` +
			`<td><a href="/quotes/` + q.ID + `" class="btn btn-primary btn-sm">Review →</a></td>` +
			`</tr>`
	}
	if rows == "" {
		rows = `<tr><td colspan="7" class="empty">✓ All caught up. No items pending review.</td></tr>`
	}

	content := `<div class="card">` +
		`<div class="queue-head"><h2>` + title + `</h2>` + countBadge + `</div>` +
		`<table>` +
		`<thead><tr>` +
		`<th>Quote</th><th>RFQ</th>` +
		`<th class="text-right">Cost (KES)</th>` +
		`<th class="text-right">Price (KES)</th>` +
		`<th class="text-right">Margin</th>` +
		`<th>Submitted</th><th>Action</th>` +
		`</tr></thead>` +
		`<tbody>` + rows + `</tbody>` +
		`</table>` +
		`</div>`

	renderLayout(w, r, title, content)
}

// ============================================================================
// ADMIN
// ============================================================================

func (a *app) handleAdmin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	configs, err := a.store.ListPricingConfigs(ctx)
	if err != nil {
		configs = []store.PricingConfig{}
	}

	var globalCfg *store.PricingConfig
	for i := range configs {
		if configs[i].RouteKey == nil && configs[i].CustomerKey == nil {
			globalCfg = &configs[i]
			break
		}
	}
	if globalCfg == nil {
		globalCfg = &store.PricingConfig{
			EPRADieselRate:                164.30,
			FuelConsumptionKMPerL:         4.5,
			DepreciationRatePerDay:        2500,
			MaintenanceRatePerKM:          12,
			InsuranceRatePerDay:           300,
			DriverRatePerDay:              3500,
			OtherCostsFlat:                5000,
			AutoApproveMarginFloor:        0.30,
			ManagerReviewMarginFloor:      0.18,
			DirectorEscalationMarginFloor: 0.10,
		}
	}

	rows := ""
	for _, c := range configs {
		rk := "GLOBAL"
		if c.RouteKey != nil {
			rk = *c.RouteKey
		}
		ck := "—"
		if c.CustomerKey != nil {
			ck = *c.CustomerKey
		}
		target := "—"
		if c.TargetMarginPct != nil {
			target = fmt.Sprintf("%.2f%%", (*c.TargetMarginPct)*100)
		}
		canDelete := c.RouteKey != nil || c.CustomerKey != nil
		deleteBtn := ""
		if canDelete {
			deleteBtn = `<form class="inline" method="post" action="/admin/pricing-configs/` + c.ID + `/delete" onsubmit="return confirm('Delete this pricing config?')"><button class="btn btn-danger btn-sm" type="submit">Delete</button></form>`
		}
		rkH := template.HTMLEscapeString(rk)
		ckH := template.HTMLEscapeString(ck)
		targetH := template.HTMLEscapeString(target)
		epraH := fmt.Sprintf("%.2f", c.EPRADieselRate)
		autoH := fmt.Sprintf("%.2f%%", c.AutoApproveMarginFloor*100)
		mgrH := fmt.Sprintf("%.2f%%", c.ManagerReviewMarginFloor*100)
		updH := c.UpdatedAt.Format("02 Jan 06")
		rows += `<tr>` +
			`<td style="font-weight:600">` + rkH + `</td>` +
			`<td>` + ckH + `</td>` +
			`<td style="text-align:right">` + epraH + `</td>` +
			`<td style="text-align:right">` + targetH + `</td>` +
			`<td style="text-align:right">` + autoH + `</td>` +
			`<td style="text-align:right">` + mgrH + `</td>` +
			`<td class="muted">` + updH + `</td>` +
			`<td>` + deleteBtn + `</td>` +
			`</tr>`
	}

	f := func(v float64) string { return fmt.Sprintf("%.2f", v) }
	f4 := func(v float64) string { return fmt.Sprintf("%.4f", v) }
	pct := func(v float64) string { return fmt.Sprintf("%.2f", v*100) }

	content := ""
	content += `<div class="alert alert-danger mb-4">⚠ Admin only — changes here take effect on the next quote calculated. No restart required.</div>`

	content += `<div class="grid-2">`
	content += `<div class="card">`
	content += `<div class="card-header"><span class="card-title">Route-Specific Rate Override</span><span class="card-eyebrow">Live Config</span></div>`
	content += `<p class="note mb-4">Override cost rates for a specific origin-destination route. Leave route blank to use global.</p>`
	content += `<form method="post" action="/admin/pricing-configs/upsert">`
	content += `<input type="hidden" name="config_type" value="route">`
	content += `<div class="field"><label>Route Key (e.g. Nairobi-Kisumu)</label><input type="text" name="route_key" placeholder="Nairobi-Kisumu"></div>`
	content += `<div class="grid-2">`
	content += `<div class="field"><label>EPRA Diesel (KES/L)</label><input type="number" step="any" name="epra_diesel_rate" value="` + f(globalCfg.EPRADieselRate) + `"></div>`
	content += `<div class="field"><label>Fuel Cons (km/L)</label><input type="number" step="any" name="fuel_consumption" value="` + f(globalCfg.FuelConsumptionKMPerL) + `"></div>`
	content += `<div class="field"><label>Depreciation/day (KES)</label><input type="number" step="any" name="depreciation" value="` + f(globalCfg.DepreciationRatePerDay) + `"></div>`
	content += `<div class="field"><label>Maintenance/km (KES)</label><input type="number" step="any" name="maintenance" value="` + f4(globalCfg.MaintenanceRatePerKM) + `"></div>`
	content += `<div class="field"><label>Insurance/day (KES)</label><input type="number" step="any" name="insurance" value="` + f(globalCfg.InsuranceRatePerDay) + `"></div>`
	content += `<div class="field"><label>Driver/day (KES)</label><input type="number" step="any" name="driver" value="` + f(globalCfg.DriverRatePerDay) + `"></div>`
	content += `<div class="field"><label>Other Flat (KES)</label><input type="number" step="any" name="other" value="` + f(globalCfg.OtherCostsFlat) + `"></div>`
	content += `<div class="field"><label>Target Margin % (optional)</label><input type="number" step="any" name="target_margin" placeholder="20"></div>`
	content += `</div>`
	content += `<button class="btn btn-primary" type="submit">Save Route Override</button>`
	content += `</form></div>`

	content += `<div class="card">`
	content += `<div class="card-header"><span class="card-title">Customer Pricing Agreement</span><span class="card-eyebrow">Negotiated</span></div>`
	content += `<p class="note mb-4">Negotiated rates for a specific customer (e.g. target_margin override, customer-specific EPRA or fuel assumptions).</p>`
	content += `<form method="post" action="/admin/pricing-configs/upsert">`
	content += `<input type="hidden" name="config_type" value="customer">`
	content += `<div class="field"><label>Customer Key</label><input type="text" name="customer_key" placeholder="Acme Corp"></div>`
	content += `<div class="field"><label>Target Margin % override</label><input type="number" step="any" name="target_margin" placeholder="15"></div>`
	content += `<div class="grid-2">`
	content += `<div class="field"><label>EPRA Diesel (KES/L) - optional</label><input type="number" step="any" name="epra_diesel_rate" placeholder="` + f(globalCfg.EPRADieselRate) + `"></div>`
	content += `<div class="field"><label>Fuel Cons (km/L) - optional</label><input type="number" step="any" name="fuel_consumption" placeholder="` + f(globalCfg.FuelConsumptionKMPerL) + `"></div>`
	content += `<div class="field"><label>Depreciation/day - optional</label><input type="number" step="any" name="depreciation" placeholder="` + f(globalCfg.DepreciationRatePerDay) + `"></div>`
	content += `<div class="field"><label>Maintenance/km - optional</label><input type="number" step="any" name="maintenance" placeholder="` + f(globalCfg.MaintenanceRatePerKM) + `"></div>`
	content += `</div>`
	content += `<button class="btn btn-secondary" type="submit">Save Customer Agreement</button>`
	content += `</form></div>`
	content += `</div>`

	content += `<div class="card">`
	content += `<div class="card-header"><span class="card-title">Global Margin Thresholds</span><span class="card-eyebrow">Approval Bands — Live</span></div>`
	content += `<p class="note mb-4">Adjust the approval-tier boundaries. Updated on the GLOBAL config row.</p>`
	content += `<form method="post" action="/admin/pricing-configs/upsert">`
	content += `<input type="hidden" name="config_type" value="thresholds">`
	content += `<div class="grid-3">`
	content += `<div class="field"><label>Auto-approve if margin &ge; (%)</label><input type="number" step="any" name="auto_approve" value="` + pct(globalCfg.AutoApproveMarginFloor) + `"></div>`
	content += `<div class="field"><label>Manager review if margin &ge; (%)</label><input type="number" step="any" name="manager_review" value="` + pct(globalCfg.ManagerReviewMarginFloor) + `"></div>`
	content += `<div class="field"><label>Director escalation if margin &ge; (%)</label><input type="number" step="any" name="director_escalation" value="` + pct(globalCfg.DirectorEscalationMarginFloor) + `"></div>`
	content += `</div>`
	content += `<button class="btn btn-danger" type="submit">Update Global Thresholds</button>`
	content += `</form></div>`

	content += `<div class="card">`
	content += `<div class="card-header"><span class="card-title">All Pricing Configs</span><span class="card-eyebrow">` + fmt.Sprintf("%d rows", len(configs)) + `</span></div>`
	content += `<table>`
	content += `<thead><tr><th>Route</th><th>Customer</th><th style="text-align:right">EPRA</th><th style="text-align:right">Target Margin</th><th style="text-align:right">Auto-Approve</th><th style="text-align:right">Mgr Floor</th><th>Updated</th><th></th></tr></thead>`
	content += `<tbody>` + rows + `</tbody>`
	content += `</table></div>`

	renderLayout(w, r, "Admin Config", content)
}

func parseFormFloat(r *http.Request, key string) (float64, bool) {
	s := strings.TrimSpace(r.FormValue(key))
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func (a *app) handleAdminUpsertConfig(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "parse", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	configType := r.FormValue("config_type")

	cfg := &store.PricingConfig{
		UpdatedBy: ptr(GetCurrentRole(r) + " (admin)"),
	}

	if v, ok := parseFormFloat(r, "epra_diesel_rate"); ok {
		cfg.EPRADieselRate = v
	}
	if v, ok := parseFormFloat(r, "fuel_consumption"); ok {
		cfg.FuelConsumptionKMPerL = v
	}
	if v, ok := parseFormFloat(r, "depreciation"); ok {
		cfg.DepreciationRatePerDay = v
	}
	if v, ok := parseFormFloat(r, "maintenance"); ok {
		cfg.MaintenanceRatePerKM = v
	}
	if v, ok := parseFormFloat(r, "insurance"); ok {
		cfg.InsuranceRatePerDay = v
	}
	if v, ok := parseFormFloat(r, "driver"); ok {
		cfg.DriverRatePerDay = v
	}
	if v, ok := parseFormFloat(r, "other"); ok {
		cfg.OtherCostsFlat = v
	}
	if v, ok := parseFormFloat(r, "target_margin"); ok {
		v = v / 100.0
		cfg.TargetMarginPct = &v
	}

	switch configType {
	case "route":
		rk := r.FormValue("route_key")
		if rk != "" {
			cfg.RouteKey = &rk
		}
	case "customer":
		ck := r.FormValue("customer_key")
		if ck != "" {
			cfg.CustomerKey = &ck
		}
	case "thresholds":
		if v, ok := parseFormFloat(r, "auto_approve"); ok {
			cfg.AutoApproveMarginFloor = v / 100.0
		}
		if v, ok := parseFormFloat(r, "manager_review"); ok {
			cfg.ManagerReviewMarginFloor = v / 100.0
		}
		if v, ok := parseFormFloat(r, "director_escalation"); ok {
			cfg.DirectorEscalationMarginFloor = v / 100.0
		}
	}

	if err := a.store.UpsertPricingConfig(ctx, cfg); err != nil {
		setFlash(w, "ER Pricing config upsert failed: "+err.Error())
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	label := configType
	if cfg.RouteKey != nil {
		label = "route " + *cfg.RouteKey
	} else if cfg.CustomerKey != nil {
		label = "customer " + *cfg.CustomerKey
	}
	setFlash(w, "OK Pricing config saved: "+label)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *app) handleAdminDeleteConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.store.DeletePricingConfig(r.Context(), id); err != nil {
		setFlash(w, "ER Delete failed: "+err.Error())
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	setFlash(w, "OK Pricing config deleted: "+id[:8])
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func ptr(s string) *string {
	return &s
}
