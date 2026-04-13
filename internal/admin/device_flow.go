package admin

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/store"
)

//go:embed templates/device_verify.html templates/device_success.html templates/device_error.html
var deviceTemplateFS embed.FS

// userCodeCharset contains only consonants with ambiguous characters removed.
const userCodeCharset = "BCDFGHJKLMNPQRSTVWXZ"

// DeviceFlowHandler handles OAuth 2.0 Device Authorization Grant (RFC 8628) endpoints.
type DeviceFlowHandler struct {
	store     *store.Store
	encKey    []byte
	cfg       *config.Config
	templates *template.Template
}

// deviceVerifyData is passed to the device_verify.html template.
type deviceVerifyData struct {
	UserCode string
	Error    string
}

// deviceErrorData is passed to the device_error.html template.
type deviceErrorData struct {
	Error string
}

// NewDeviceFlowHandler constructs a DeviceFlowHandler and parses templates.
func NewDeviceFlowHandler(st *store.Store, encKey []byte, cfg *config.Config) (*DeviceFlowHandler, error) {
	tmpl, err := template.ParseFS(deviceTemplateFS,
		"templates/device_verify.html",
		"templates/device_success.html",
		"templates/device_error.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parse device flow templates: %w", err)
	}
	return &DeviceFlowHandler{
		store:     st,
		encKey:    encKey,
		cfg:       cfg,
		templates: tmpl,
	}, nil
}

// HandleDeviceAuthorize handles POST /oauth/device/code.
// Creates a new device_code + user_code pair and returns them per RFC 8628.
func (h *DeviceFlowHandler) HandleDeviceAuthorize(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientID string `json:"client_id"`
		Scope    string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}

	scopes := strings.Fields(body.Scope)
	if len(scopes) == 0 {
		scopes = []string{"project:inference", "offline_access"}
	}

	deviceCode, err := generateDeviceCode()
	if err != nil {
		log.Printf("ERROR device_flow: generate device code: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}

	userCode, err := generateUserCode()
	if err != nil {
		log.Printf("ERROR device_flow: generate user code: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}

	nonce, err := generateNonce()
	if err != nil {
		log.Printf("ERROR device_flow: generate nonce: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}

	codeTTL := h.cfg.Auth.OAuth.Hydra.DeviceFlow.CodeTTL
	if codeTTL <= 0 {
		codeTTL = 600
	}
	pollInterval := h.cfg.Auth.OAuth.Hydra.DeviceFlow.PollInterval
	if pollInterval <= 0 {
		pollInterval = 5
	}

	dc := &store.DeviceCode{
		DeviceCode:        deviceCode,
		UserCode:          userCode,
		ClientID:          body.ClientID,
		Scopes:            scopes,
		VerificationNonce: nonce,
		ExpiresAt:         time.Now().Add(time.Duration(codeTTL) * time.Second),
		PollInterval:      pollInterval,
	}

	if err := h.store.CreateDeviceCode(r.Context(), dc); err != nil {
		log.Printf("ERROR device_flow: create device code: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}

	verificationURI := baseURL(r) + "/oauth/device"
	formattedCode := formatUserCode(userCode)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"device_code":               deviceCode,
		"user_code":                 formattedCode,
		"verification_uri":          verificationURI,
		"verification_uri_complete": verificationURI + "?user_code=" + url.QueryEscape(formattedCode),
		"expires_in":                codeTTL,
		"interval":                  pollInterval,
	})
}

// HandleVerificationPage handles GET /oauth/device.
// Renders the user code input form.
func (h *DeviceFlowHandler) HandleVerificationPage(w http.ResponseWriter, r *http.Request) {
	userCode := r.URL.Query().Get("user_code")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "device_verify.html", deviceVerifyData{
		UserCode: userCode,
	}); err != nil {
		log.Printf("ERROR device_flow: render template: %v", err)
	}
}

// HandleVerifyUserCode handles POST /oauth/device.
// Validates the user code and redirects to Hydra's authorization endpoint.
func (h *DeviceFlowHandler) HandleVerifyUserCode(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderVerifyError(w, "", "Invalid form data.")
		return
	}

	rawCode := r.FormValue("user_code")
	normalized := normalizeUserCode(rawCode)

	if normalized == "" {
		h.renderVerifyError(w, rawCode, "Please enter a code.")
		return
	}

	dc, err := h.store.GetDeviceCodeByUserCode(r.Context(), normalized)
	if err != nil {
		log.Printf("ERROR device_flow: lookup user code: %v", err)
		h.renderVerifyError(w, rawCode, "Something went wrong. Please try again.")
		return
	}
	if dc == nil {
		h.renderVerifyError(w, rawCode, "Invalid or expired code. Please check and try again.")
		return
	}

	// Build Hydra authorization URL.
	dfCfg := h.cfg.Auth.OAuth.Hydra.DeviceFlow
	hydraPublicURL := strings.TrimRight(h.cfg.Auth.OAuth.Hydra.PublicURL, "/")

	redirectURI := baseURL(r) + "/oauth/device/callback"
	scope := strings.Join(dc.Scopes, " ")

	authURL := fmt.Sprintf("%s/oauth2/auth?response_type=code&client_id=%s&redirect_uri=%s&scope=%s&state=%s",
		hydraPublicURL,
		url.QueryEscape(dfCfg.ClientID),
		url.QueryEscape(redirectURI),
		url.QueryEscape(scope),
		url.QueryEscape(dc.VerificationNonce),
	)

	http.Redirect(w, r, authURL, http.StatusFound)
}

// HandleCallback handles GET /oauth/device/callback.
// Receives the auth code from Hydra, exchanges it for tokens, and stores them.
func (h *DeviceFlowHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		h.renderErrorPage(w, "Missing state parameter.")
		return
	}

	ctx := r.Context()

	dc, err := h.store.GetDeviceCodeByNonce(ctx, state)
	if err != nil {
		log.Printf("ERROR device_flow: lookup nonce: %v", err)
		h.renderErrorPage(w, "Something went wrong. Please try again.")
		return
	}
	if dc == nil {
		h.renderErrorPage(w, "Invalid or expired authorization request.")
		return
	}

	// Check for error from Hydra (user denied consent).
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		_ = h.store.DenyDeviceCode(ctx, dc.ID)
		desc := r.URL.Query().Get("error_description")
		if desc == "" {
			desc = "Authorization was denied."
		}
		h.renderErrorPage(w, desc)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		h.renderErrorPage(w, "Missing authorization code.")
		return
	}

	// Exchange auth code for tokens via Hydra's token endpoint.
	dfCfg := h.cfg.Auth.OAuth.Hydra.DeviceFlow
	hydraPublicURL := strings.TrimRight(h.cfg.Auth.OAuth.Hydra.PublicURL, "/")
	redirectURI := baseURL(r) + "/oauth/device/callback"

	tokenResp, err := exchangeAuthCode(ctx, hydraPublicURL, dfCfg.ClientID, dfCfg.ClientSecret, code, redirectURI)
	if err != nil {
		log.Printf("ERROR device_flow: exchange auth code: %v", err)
		h.renderErrorPage(w, "Failed to complete authorization. Please try again.")
		return
	}

	// Encrypt tokens before storage.
	encAccessToken, err := crypto.Encrypt(h.encKey, []byte(tokenResp.AccessToken))
	if err != nil {
		log.Printf("ERROR device_flow: encrypt access token: %v", err)
		h.renderErrorPage(w, "Internal error. Please try again.")
		return
	}
	encRefreshToken, err := crypto.Encrypt(h.encKey, []byte(tokenResp.RefreshToken))
	if err != nil {
		log.Printf("ERROR device_flow: encrypt refresh token: %v", err)
		h.renderErrorPage(w, "Internal error. Please try again.")
		return
	}

	if err := h.store.ApproveDeviceCode(ctx, dc.ID, encAccessToken, encRefreshToken, tokenResp.TokenType, tokenResp.ExpiresIn); err != nil {
		log.Printf("ERROR device_flow: approve device code: %v", err)
		h.renderErrorPage(w, "Failed to save authorization. Please try again.")
		return
	}

	// Render success page.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "device_success.html", nil); err != nil {
		log.Printf("ERROR device_flow: render template: %v", err)
	}
}

// HandleTokenPoll handles POST /oauth/device/token.
// CLI clients poll this endpoint to retrieve tokens after user approval.
func (h *DeviceFlowHandler) HandleTokenPoll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		GrantType  string `json:"grant_type"`
		DeviceCode string `json:"device_code"`
		ClientID   string `json:"client_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}

	if body.GrantType != "urn:ietf:params:oauth:grant-type:device_code" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		return
	}

	if body.DeviceCode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}

	ctx := r.Context()

	dc, err := h.store.GetDeviceCodeByCode(ctx, body.DeviceCode)
	if err != nil {
		log.Printf("ERROR device_flow: lookup device code: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	if dc == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}

	// Check expiry.
	if time.Now().After(dc.ExpiresAt) {
		_ = h.store.DeleteDeviceCode(ctx, dc.ID)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expired_token"})
		return
	}

	switch dc.Status {
	case "denied":
		_ = h.store.DeleteDeviceCode(ctx, dc.ID)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "access_denied"})
		return

	case "pending":
		// Check slow_down: if polled faster than the interval.
		slowDown := false
		if dc.LastPolledAt != nil {
			elapsed := time.Since(*dc.LastPolledAt)
			if elapsed < time.Duration(dc.PollInterval)*time.Second {
				slowDown = true
			}
		}
		_ = h.store.UpdateDeviceCodePoll(ctx, dc.ID, slowDown)
		if slowDown {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slow_down"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "authorization_pending"})
		return

	case "approved":
		// Atomically consume the approved code to prevent replay.
		consumed, err := h.store.ConsumeApprovedDeviceCode(ctx, body.DeviceCode)
		if err != nil {
			log.Printf("ERROR device_flow: consume approved device code: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
			return
		}
		if consumed == nil {
			// Another concurrent poll already consumed it.
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		}

		accessToken, err := crypto.Decrypt(h.encKey, consumed.AccessToken)
		if err != nil {
			log.Printf("ERROR device_flow: decrypt access token: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
			return
		}
		refreshToken, err := crypto.Decrypt(h.encKey, consumed.RefreshToken)
		if err != nil {
			log.Printf("ERROR device_flow: decrypt refresh token: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"access_token":  string(accessToken),
			"refresh_token": string(refreshToken),
			"token_type":    consumed.TokenType,
			"expires_in":    consumed.TokenExpiresIn,
		})
		return

	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
	}
}

// --- Helpers ---

// generateDeviceCode returns a 64-char hex string (32 bytes of randomness).
func generateDeviceCode() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// generateUserCode returns an 8-char string from the consonant charset.
func generateUserCode() (string, error) {
	b := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	code := make([]byte, 8)
	for i := range code {
		code[i] = userCodeCharset[int(b[i])%len(userCodeCharset)]
	}
	return string(code), nil
}

// generateNonce returns a 32-char hex string (16 bytes of randomness).
func generateNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// formatUserCode inserts a dash in the middle: "ABCDEFGH" -> "ABCD-EFGH".
func formatUserCode(code string) string {
	if len(code) != 8 {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// normalizeUserCode strips dashes/spaces and uppercases the input.
func normalizeUserCode(raw string) string {
	s := strings.ToUpper(raw)
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

// hydraTokenResponse represents the JSON response from Hydra's token endpoint.
type hydraTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

// exchangeAuthCode exchanges an authorization code for tokens via Hydra's token endpoint.
func exchangeAuthCode(ctx context.Context, hydraPublicURL, clientID, clientSecret, code, redirectURI string) (*hydraTokenResponse, error) {
	endpoint := hydraPublicURL + "/oauth2/token"

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp hydraTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	return &tokenResp, nil
}

// renderVerifyError re-renders the verification form with an error message.
func (h *DeviceFlowHandler) renderVerifyError(w http.ResponseWriter, userCode, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "device_verify.html", deviceVerifyData{
		UserCode: userCode,
		Error:    errMsg,
	}); err != nil {
		log.Printf("ERROR device_flow: render template: %v", err)
	}
}

// renderErrorPage renders the device_error.html template.
func (h *DeviceFlowHandler) renderErrorPage(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "device_error.html", deviceErrorData{
		Error: errMsg,
	}); err != nil {
		log.Printf("ERROR device_flow: render template: %v", err)
	}
}
