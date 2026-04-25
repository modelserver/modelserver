package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/modelcatalog"
	"github.com/modelserver/modelserver/internal/types"
	"github.com/tidwall/sjson"
)

const ctxModel contextKey = "model"

// ModelFromContext returns the resolved catalog Model the request has been
// matched to. Nil when no model could be resolved (e.g. GET endpoints, or
// request bodies the middleware doesn't parse).
func ModelFromContext(ctx context.Context) *types.Model {
	if m, ok := ctx.Value(ctxModel).(*types.Model); ok {
		return m
	}
	return nil
}

// ResolveModelMiddleware peeks the request to extract the client-supplied
// model name, looks it up in the catalog, and stores the canonical Model
// on the context. Downstream middlewares (SubscriptionEligibility, Guard,
// Executor billing) read from here so they don't re-parse the body.
//
// The middleware is intentionally permissive on failure: unknown or disabled
// models fall through unchanged, because the handlers still need to render
// the provider-specific error envelope the clients expect. A missing model
// therefore means ModelFromContext returns nil.
func ResolveModelMiddleware(catalog modelcatalog.Catalog, maxBodySize int64, multipartMaxBodySize int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if catalog == nil {
				next.ServeHTTP(w, r)
				return
			}

			raw := extractModelFromRequest(r, maxBodySize, multipartMaxBodySize)
			if raw == "" {
				next.ServeHTTP(w, r)
				return
			}
			m, ok := catalog.Lookup(raw)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			ctx := context.WithValue(r.Context(), ctxModel, m)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractModelFromRequest inspects the URL path (Gemini) or JSON body (every
// other endpoint) to read the client-supplied model string. The body is
// restored so downstream handlers can re-read it.
func extractModelFromRequest(r *http.Request, maxBodySize int64, multipartMaxBodySize int64) string {
	// Gemini: /v1beta/models/{model}:{method} — extract from the wildcard.
	if strings.HasPrefix(r.URL.Path, "/v1beta/models/") {
		wildcard := chi.URLParam(r, "*")
		if wildcard == "" {
			wildcard = strings.TrimPrefix(r.URL.Path, "/v1beta/models/")
		}
		if i := strings.LastIndex(wildcard, ":"); i >= 0 {
			return wildcard[:i]
		}
		if i := strings.LastIndex(wildcard, "/"); i >= 0 {
			return wildcard[:i]
		}
		return ""
	}

	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if strings.EqualFold(mediaType, "multipart/form-data") {
		return extractModelFromMultipart(r, multipartMaxBodySize)
	}

	// Body-based endpoints: read, peek JSON, restore.
	if r.Body == nil {
		return ""
	}
	limit := maxBodySize
	if limit <= 0 {
		limit = 16 << 20 // 16 MiB safety cap if unspecified.
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return ""
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if int64(len(body)) > limit {
		return ""
	}
	var shape struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &shape)
	return shape.Model
}

func extractModelFromMultipart(r *http.Request, maxBodySize int64) string {
	if r.Body == nil {
		return ""
	}
	limit := maxBodySize
	if limit <= 0 {
		limit = 200 << 20
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return ""
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if int64(len(body)) > limit {
		return ""
	}
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || params["boundary"] == "" {
		return ""
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := mr.NextPart()
		if err != nil {
			return ""
		}
		if part.FormName() != "model" {
			continue
		}
		val, _ := io.ReadAll(io.LimitReader(part, 256))
		return strings.TrimSpace(string(val))
	}
}

// RewriteRequestBodyModel rewrites the `model` JSON field in the request body
// to the canonical name. Handlers call this to keep the body consistent with
// the resolved model after alias normalization. Safe to call when the body
// already matches the canonical name (no-op writes back the same JSON).
func RewriteRequestBodyModel(r *http.Request, canonical string) {
	if r.Body == nil {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}
	newBody, err := sjson.SetBytes(body, "model", canonical)
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(newBody))
	r.ContentLength = int64(len(newBody))
}
