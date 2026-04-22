package store

import (
	"strings"
	"testing"
)

func TestRouteSelectColsContainsRequestKinds(t *testing.T) {
	// Guards against future drift between scanRoute's Scan call and
	// the SELECT column list — if request_kinds is removed from the
	// SELECT but Scan still expects it, every list call will runtime-fail.
	if !strings.Contains(routeSelectCols, "request_kinds") {
		t.Error("routeSelectCols must include request_kinds")
	}
}
