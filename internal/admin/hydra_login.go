package admin

import (
	"embed"
	"html/template"
	"log"
	"net/http"
	"net/url"

	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/store"
)

//go:embed templates/login.html
var loginTemplateFS embed.FS

// LoginHandler handles Hydra's login provider endpoint.
type LoginHandler struct {
	hydra     *HydraClient
	store     *store.Store
	encKey    []byte
	cfg       *config.Config
	templates *template.Template
}

// loginTemplateData is the data passed to the login HTML template.
type loginTemplateData struct {
	Challenge string
	Error     string
	Providers []loginProvider
	RetryURL  string
}

// loginProvider represents a single OAuth provider shown on the login page.
type loginProvider struct {
	Label string
	URL   string
}

// NewLoginHandler constructs a LoginHandler and parses the embedded template.
// Returns an error if the template cannot be parsed.
func NewLoginHandler(hydra *HydraClient, st *store.Store, encKey []byte, cfg *config.Config) (*LoginHandler, error) {
	tmpl, err := template.ParseFS(loginTemplateFS, "templates/login.html")
	if err != nil {
		return nil, err
	}
	return &LoginHandler{
		hydra:     hydra,
		store:     st,
		encKey:    encKey,
		cfg:       cfg,
		templates: tmpl,
	}, nil
}

// ServeHTTP handles GET /oauth/login?login_challenge=...
//
// Flow:
//  1. Fetch login request from Hydra to validate the challenge.
//  2. If Hydra says Skip=true, accept immediately (existing Hydra session).
//  3. Check for an existing modelserver session cookie.
//  4. If a valid session exists, accept the Hydra login with that user.
//  5. Otherwise, render the login page with available OAuth provider links.
func (h *LoginHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	challenge := r.URL.Query().Get("login_challenge")
	if challenge == "" {
		http.Error(w, "missing login_challenge", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Step 1: Fetch login request from Hydra.
	loginReq, err := h.hydra.GetLoginRequest(ctx, challenge)
	if err != nil {
		log.Printf("ERROR hydra_login: GetLoginRequest challenge=%s: %v", challenge, err)
		h.renderError(w, challenge, "Failed to fetch login request. Please try again.")
		return
	}

	// Step 2: Hydra says we can skip — user already has an active Hydra session.
	if loginReq.Skip {
		redirect, err := h.hydra.AcceptLogin(ctx, challenge, loginReq.Subject)
		if err != nil {
			log.Printf("ERROR hydra_login: AcceptLogin (skip) challenge=%s subject=%s: %v", challenge, loginReq.Subject, err)
			h.renderError(w, challenge, "Failed to complete login. Please try again.")
			return
		}
		http.Redirect(w, r, redirect.RedirectTo, http.StatusFound)
		return
	}

	// Step 3: Check for an existing modelserver session cookie.
	userID, ok := getOAuthSession(r, h.encKey)
	if ok && userID != "" {
		// Step 4: Accept the Hydra login with the session's user.
		redirect, err := h.hydra.AcceptLogin(ctx, challenge, userID)
		if err != nil {
			log.Printf("ERROR hydra_login: AcceptLogin challenge=%s userID=%s: %v", challenge, userID, err)
			h.renderError(w, challenge, "Failed to complete login. Please try again.")
			return
		}
		http.Redirect(w, r, redirect.RedirectTo, http.StatusFound)
		return
	}

	// Step 5: No session — render the login page.
	h.renderLoginPage(w, r, challenge, "")
}

// renderLoginPage renders the login.html template with the available OAuth providers.
// The return_to parameter embeds the current login URL so that after OAuth the user
// is redirected back here to complete the Hydra flow.
func (h *LoginHandler) renderLoginPage(w http.ResponseWriter, r *http.Request, challenge, errMsg string) {
	// Build the URL that OAuth providers should redirect back to after login.
	// This is the current page URL (with the login_challenge).
	returnTo := buildReturnToURL(r, challenge)

	providers := h.buildProviders(r, returnTo)

	data := loginTemplateData{
		Challenge: challenge,
		Error:     errMsg,
		Providers: providers,
		RetryURL:  returnTo,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "login.html", data); err != nil {
		log.Printf("ERROR hydra_login: render template: %v", err)
	}
}

// renderError renders the login page with an error message.
// It does not attempt to build provider links (no request available) but
// includes a relative retry URL so the user can try again.
func (h *LoginHandler) renderError(w http.ResponseWriter, challenge, errMsg string) {
	data := loginTemplateData{
		Challenge: challenge,
		Error:     errMsg,
		RetryURL:  "/oauth/login?login_challenge=" + url.QueryEscape(challenge),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "login.html", data); err != nil {
		log.Printf("ERROR hydra_login: render template: %v", err)
	}
}

// buildProviders returns the list of configured OAuth providers as login links.
// Each link points to the existing redirect endpoint with a return_to parameter.
func (h *LoginHandler) buildProviders(r *http.Request, returnTo string) []loginProvider {
	var providers []loginProvider

	base := baseURL(r)
	addProvider := func(name, label string) {
		u := base + "/api/v1/auth/oauth/" + name + "/redirect?return_to=" + url.QueryEscape(returnTo)
		providers = append(providers, loginProvider{Label: label, URL: u})
	}

	if h.cfg.Auth.OAuth.GitHub.ClientID != "" {
		addProvider("github", "GitHub")
	}
	if h.cfg.Auth.OAuth.Google.ClientID != "" {
		addProvider("google", "Google")
	}
	if h.cfg.Auth.OAuth.OIDC.IssuerURL != "" {
		label := "OIDC"
		if h.cfg.Auth.OAuth.OIDC.DisplayName != "" {
			label = h.cfg.Auth.OAuth.OIDC.DisplayName
		}
		addProvider("oidc", label)
	}

	return providers
}

// buildReturnToURL constructs an absolute login page URL including the login_challenge.
// Must be absolute because the frontend (code.cs.ac.cn) navigates to this URL,
// and it needs to land on the admin API domain (codeapi.cs.ac.cn), not the frontend domain.
func buildReturnToURL(r *http.Request, challenge string) string {
	if r == nil {
		return ""
	}
	q := url.Values{}
	q.Set("login_challenge", challenge)
	return baseURL(r) + "/oauth/login?" + q.Encode()
}

// baseURL returns the scheme+host of the request (e.g. "https://example.com").
func baseURL(r *http.Request) string {
	if r == nil {
		return ""
	}
	return scheme(r) + "://" + r.Host
}

// scheme returns "https" or "http" based on TLS state and X-Forwarded-Proto header.
func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		return fwd
	}
	return "http"
}
