package proxy

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/modelserver/modelserver/internal/store"
)

const (
	// CodexClientID is the public OAuth client id used by the codex CLI.
	CodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// CodexIssuerURL is the OpenAI auth issuer.
	CodexIssuerURL = "https://auth.openai.com"
	// CodexAuthURL is the OAuth authorize endpoint.
	CodexAuthURL = CodexIssuerURL + "/oauth/authorize"
	// CodexTokenURL is the OAuth token endpoint.
	CodexTokenURL = CodexIssuerURL + "/oauth/token"
	// CodexScopes is the scope list used by the codex CLI authorize flow.
	CodexScopes = "openid profile email offline_access"
	// codexExpiryBuffer triggers proactive refresh this many seconds before expiry.
	codexExpiryBuffer = 300
)

// CodexCredentials holds OAuth credentials for a codex upstream.
type CodexCredentials struct {
	IDToken          string `json:"id_token,omitempty"`
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ChatGPTAccountID string `json:"chatgpt_account_id,omitempty"`
	ExpiresAt        int64  `json:"expires_at"`
	ClientID         string `json:"client_id,omitempty"`
}

// CodexOAuthTokenManager manages OAuth tokens for codex upstreams.
type CodexOAuthTokenManager struct {
	mu            sync.RWMutex
	credentials   map[string]*CodexCredentials // upstreamID → credentials
	store         *store.Store
	encryptionKey []byte
	logger        *slog.Logger
	sfGroup       singleflight.Group
	httpClient    *http.Client
	tokenURL      string
}

// NewCodexOAuthTokenManager constructs a manager. Pass nil store / nil key
// in tests that don't exercise the persistence path.
func NewCodexOAuthTokenManager(st *store.Store, encKey []byte, logger *slog.Logger) *CodexOAuthTokenManager {
	return &CodexOAuthTokenManager{
		credentials:   make(map[string]*CodexCredentials),
		store:         st,
		encryptionKey: encKey,
		logger:        logger,
		httpClient:    &http.Client{Timeout: 15 * time.Second},
		tokenURL:      CodexTokenURL,
	}
}

// ParseCodexAccessTokenAndAccount accepts either a bare access token or a
// CodexCredentials JSON blob. Returns the bare token and account id; on a
// bare token the account id is empty.
func ParseCodexAccessTokenAndAccount(raw string) (accessToken, accountID string) {
	if len(raw) == 0 || raw[0] != '{' {
		return raw, ""
	}
	var creds CodexCredentials
	if json.Unmarshal([]byte(raw), &creds) != nil {
		return raw, ""
	}
	return creds.AccessToken, creds.ChatGPTAccountID
}

// extractChatGPTAccountIDFromIDToken decodes the middle segment of the JWT
// and returns the chatgpt_account_id claim from the OpenAI custom-namespace
// object. Returns empty when the claim is missing or the token is unparseable.
// We do NOT verify the signature — the caller obtained this token from the
// issuer over TLS in the immediately preceding exchange, and we are only
// extracting an opaque identifier for routing purposes.
func extractChatGPTAccountIDFromIDToken(idToken string) string {
	parts := splitJWT(idToken)
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Auth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Auth.ChatGPTAccountID
}

func splitJWT(token string) []string {
	out := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			out = append(out, token[start:i])
			start = i + 1
		}
	}
	out = append(out, token[start:])
	return out
}
