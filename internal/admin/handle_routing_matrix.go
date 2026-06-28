package admin

import (
	"net/http"
	"sort"

	"github.com/modelserver/modelserver/internal/proxy"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// matrixLister is the narrow subset of *store.Store that handleRoutingMatrix
// needs. Lets tests inject a fake without spinning up a database.
type matrixLister interface {
	ListModelsByStatus(status string) ([]types.Model, error)
}

type matrixCellOut struct {
	Model             string `json:"model"`
	Kind              string `json:"kind"`
	UpstreamGroupID   string `json:"upstream_group_id"`
	UpstreamGroupName string `json:"upstream_group_name"`
	RouteID           string `json:"route_id"`
	MatchPriority     int    `json:"match_priority"`
}

type matrixResponse struct {
	Models []string        `json:"models"`
	Kinds  []string        `json:"kinds"`
	Cells  []matrixCellOut `json:"cells"`
}

// handleRoutingMatrix is the production binding: store + router from main.
func handleRoutingMatrix(st *store.Store, router *proxy.Router) http.HandlerFunc {
	return handleRoutingMatrixWithLister(st.ListModelsByStatus, router)
}

// handleRoutingMatrixWithLister is the testable form: an injectable model
// lister + the live router. The router's MatrixGlobal mirrors the proxy's
// own route-walking, so the matrix cannot drift from runtime behavior.
func handleRoutingMatrixWithLister(
	listModels func(string) ([]types.Model, error),
	router *proxy.Router,
) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		models, err := listModels(types.ModelStatusActive)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list models")
			return
		}
		names := make([]string, len(models))
		for i, m := range models {
			names[i] = m.Name
		}
		sort.Strings(names)

		kinds := append([]string(nil), types.AllRequestKinds...)
		sort.Strings(kinds)

		cells := router.MatrixGlobal(names, "")
		groupNames := router.SnapshotGroupNames()

		out := matrixResponse{
			Models: names,
			Kinds:  kinds,
			Cells:  make([]matrixCellOut, 0, len(cells)),
		}
		for _, c := range cells {
			out.Cells = append(out.Cells, matrixCellOut{
				Model:             c.Model,
				Kind:              c.Kind,
				UpstreamGroupID:   c.UpstreamGroupID,
				UpstreamGroupName: groupNames[c.UpstreamGroupID],
				RouteID:           c.RouteID,
				MatchPriority:     c.MatchPriority,
			})
		}
		writeData(w, http.StatusOK, out)
	}
}
