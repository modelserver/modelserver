package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/modelserver/modelserver/internal/proxy"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// fakeListModels is a shared model lister used by matrix tests.
func fakeListModels(_ string) ([]types.Model, error) {
	return []types.Model{
		{Name: "claude-sonnet", Status: types.ModelStatusActive},
		{Name: "gpt-5", Status: types.ModelStatusActive},
	}, nil
}

// fakeRouter builds the shared proxy.Router used by matrix tests.
// Routes have no Clients filter so every client bucket resolves.
func fakeRouter() *proxy.Router {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	upstreams := []types.Upstream{
		{ID: "up-a", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"claude-sonnet"}},
		{ID: "up-b", Provider: types.ProviderOpenAI, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"gpt-5"}},
	}
	groups := []store.UpstreamGroupWithMembers{
		{
			UpstreamGroup: types.UpstreamGroup{ID: "grp-anth", Name: "anthropic-pool", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members: []store.UpstreamGroupMemberDetail{
				{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp-anth", UpstreamID: "up-a"}},
			},
		},
		{
			UpstreamGroup: types.UpstreamGroup{ID: "grp-oai", Name: "openai-pool", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members: []store.UpstreamGroupMemberDetail{
				{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp-oai", UpstreamID: "up-b"}},
			},
		},
	}
	routes := []types.Route{
		{ID: "rt-1", ProjectID: "", ModelNames: []string{"claude-sonnet"}, RequestKinds: []string{types.KindAnthropicMessages}, UpstreamGroupID: "grp-anth", MatchPriority: 100, Status: "active"},
		{ID: "rt-2", ProjectID: "", ModelNames: []string{"gpt-5"}, RequestKinds: []string{types.KindOpenAIChatCompletions, types.KindOpenAIResponses}, UpstreamGroupID: "grp-oai", MatchPriority: 5, Status: "active"},
	}
	return proxy.NewRouter(upstreams, groups, routes, []byte{}, logger, time.Minute, nil, nil, nil)
}

func TestHandleRoutingMatrix_HappyPath(t *testing.T) {
	h := handleRoutingMatrixWithLister(fakeListModels, fakeRouter())
	req := httptest.NewRequest(http.MethodGet, "/routing/matrix", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Models []string `json:"models"`
			Kinds  []string `json:"kinds"`
			Cells  []struct {
				Model             string `json:"model"`
				Kind              string `json:"kind"`
				UpstreamGroupID   string `json:"upstream_group_id"`
				UpstreamGroupName string `json:"upstream_group_name"`
				RouteID           string `json:"route_id"`
				MatchPriority     int    `json:"match_priority"`
			} `json:"cells"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}

	// Alphabetical ordering of models and kinds.
	want := []string{"claude-sonnet", "gpt-5"}
	if !equalStrings(resp.Data.Models, want) {
		t.Errorf("models = %v, want %v", resp.Data.Models, want)
	}
	gotKinds := append([]string(nil), resp.Data.Kinds...)
	sortedKinds := append([]string(nil), types.AllRequestKinds...)
	sort.Strings(sortedKinds)
	if !equalStrings(gotKinds, sortedKinds) {
		t.Errorf("kinds = %v, want %v (sorted AllRequestKinds)", gotKinds, sortedKinds)
	}

	// Sparse cells: 3 (model,kind) pairs × 5 client buckets = 15 total.
	// Routes have no Clients filter so every client bucket resolves.
	if len(resp.Data.Cells) != 15 {
		t.Errorf("len(cells) = %d, want 15 (3 model×kind pairs × 5 client buckets)", len(resp.Data.Cells))
	}
	for _, c := range resp.Data.Cells {
		if c.UpstreamGroupName == "" {
			t.Errorf("cell %s/%s missing upstream_group_name", c.Model, c.Kind)
		}
	}
}

func TestHandleRoutingMatrix_FilterByClient(t *testing.T) {
	h := handleRoutingMatrixWithLister(fakeListModels, fakeRouter())

	req := httptest.NewRequest(http.MethodGet, "/routing/matrix?client=claude-code-cli", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Data struct {
			Cells []matrixCellOut `json:"cells"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, c := range resp.Data.Cells {
		if c.Client != types.ClientBucketClaudeCodeCLI {
			t.Errorf("filter leaked: cell.Client = %q", c.Client)
		}
	}
}

func TestHandleRoutingMatrix_FilterRejectsInvalid(t *testing.T) {
	h := handleRoutingMatrixWithLister(fakeListModels, fakeRouter())

	req := httptest.NewRequest(http.MethodGet, "/routing/matrix?client=bogus", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid client: status = %d, want 400", rec.Code)
	}
}
