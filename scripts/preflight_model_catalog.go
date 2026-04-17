// preflight_model_catalog.go
//
// Run before deploying migration 016_model_catalog.sql against production.
// Connects READ-ONLY to the target database and surfaces every situation in
// which the migration would silently break routing or 400 traffic that
// previously succeeded:
//
//   1. routes.model_pattern values that the SQL glob-to-LIKE rewrite cannot
//      represent (?, character classes) — these become orphan routes.
//   2. routes whose pattern would expand to an empty model_names array given
//      the current upstream/group state — these become dead routes.
//   3. mixed-case names in upstreams.supported_models / api_keys.allowed_models
//      / plan & policy rate-map keys — these are normalized by the migration
//      itself, but the report makes the change visible.
//   4. plan / policy rate-map keys that are not present in any upstream's
//      supported_models — "orphan models" that will enter the catalog but
//      be unroutable.
//   5. recent client-supplied model names (sampled from requests.model over
//      a configurable window) that are NOT in the seed candidate set — these
//      will start returning 400 unsupported_model after deploy.
//
// Usage:
//   go run ./scripts/preflight_model_catalog.go \
//       -db "$MODELSERVER_DB_URL" -lookback 168h
//
// Exit code:
//   0 — no findings, safe to deploy.
//   1 — findings printed; review and resolve before deploy.
//   2 — connection or query error.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	dbURL := flag.String("db", os.Getenv("MODELSERVER_DB_URL"), "PostgreSQL connection URL (default: $MODELSERVER_DB_URL)")
	lookback := flag.Duration("lookback", 7*24*time.Hour, "How far back to sample requests.model for unknown-name detection")
	flag.Parse()

	if *dbURL == "" {
		fmt.Fprintln(os.Stderr, "preflight: -db or MODELSERVER_DB_URL is required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, *dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "preflight: connect: %v\n", err)
		os.Exit(2)
	}
	defer conn.Close(ctx)

	findings := 0

	findings += reportPatternsNotGlobSafe(ctx, conn)
	findings += reportWouldBeOrphanRoutes(ctx, conn)
	findings += reportMixedCaseNames(ctx, conn)
	findings += reportOrphanRateMapKeys(ctx, conn)
	findings += reportUnseededRequestModels(ctx, conn, *lookback)

	if findings == 0 {
		fmt.Println("preflight: no findings — migration 016 is safe to deploy.")
		os.Exit(0)
	}
	fmt.Printf("\npreflight: %d category(ies) of findings — review above before deploy.\n", findings)
	os.Exit(1)
}

func reportPatternsNotGlobSafe(ctx context.Context, conn *pgx.Conn) int {
	rows, err := conn.Query(ctx, `
        SELECT id, COALESCE(project_id::text, '<global>'), model_pattern
          FROM routes
         WHERE model_pattern ~ '[?\[\]]'
         ORDER BY model_pattern`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "preflight: query unsafe patterns: %v\n", err)
		return 1
	}
	defer rows.Close()

	var lines []string
	for rows.Next() {
		var id, projectID, pattern string
		_ = rows.Scan(&id, &projectID, &pattern)
		lines = append(lines, fmt.Sprintf("  - route %s (project=%s) pattern=%q", id, projectID, pattern))
	}
	if len(lines) == 0 {
		return 0
	}
	fmt.Println("[1/5] Routes with patterns the migration cannot expand (`?` or `[…]`):")
	fmt.Println(strings.Join(lines, "\n"))
	fmt.Println("      → After migration these routes will have model_names = '{}' and match nothing.")
	fmt.Println("      → Action: edit each route to a list of canonical names, or delete the route.")
	return 1
}

func reportWouldBeOrphanRoutes(ctx context.Context, conn *pgx.Conn) int {
	rows, err := conn.Query(ctx, `
        WITH expanded AS (
            SELECT r.id, r.upstream_group_id, r.model_pattern,
                COALESCE((
                    SELECT count(DISTINCT m)
                    FROM (
                        SELECT unnest(u.supported_models) AS m
                        FROM upstream_group_members gm
                        JOIN upstreams u ON u.id = gm.upstream_id
                        WHERE gm.upstream_group_id = r.upstream_group_id
                          AND u.status = 'active'
                    ) candidates
                    WHERE m = r.model_pattern
                       OR r.model_pattern = '*'
                       OR (r.model_pattern LIKE '%*%' AND m LIKE replace(r.model_pattern, '*', '%'))
                ), 0) AS match_count
            FROM routes r
        )
        SELECT id, upstream_group_id, model_pattern
          FROM expanded
         WHERE match_count = 0`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "preflight: query orphan routes: %v\n", err)
		return 1
	}
	defer rows.Close()

	var lines []string
	for rows.Next() {
		var id, groupID, pattern string
		_ = rows.Scan(&id, &groupID, &pattern)
		lines = append(lines, fmt.Sprintf("  - route %s group=%s pattern=%q", id, groupID, pattern))
	}
	if len(lines) == 0 {
		return 0
	}
	fmt.Println("\n[2/5] Routes whose pattern matches NO active-upstream model (would-be orphans):")
	fmt.Println(strings.Join(lines, "\n"))
	fmt.Println("      → After migration these routes will have model_names = '{}' and match nothing.")
	fmt.Println("      → Action: add an upstream that supports the intended model, or delete the route.")
	return 1
}

func reportMixedCaseNames(ctx context.Context, conn *pgx.Conn) int {
	type row struct{ source, value string }
	var hits []row

	queries := []struct {
		label, sql string
	}{
		{"upstreams.supported_models", `SELECT DISTINCT 'upstream:' || id, m FROM upstreams, unnest(supported_models) AS m WHERE m <> lower(m)`},
		{"api_keys.allowed_models", `SELECT DISTINCT 'api_key:' || id, m FROM api_keys, unnest(allowed_models) AS m WHERE m <> lower(m)`},
		{"plans.model_credit_rates keys", `SELECT DISTINCT 'plan:' || id, k FROM plans, jsonb_object_keys(model_credit_rates) AS k WHERE model_credit_rates IS NOT NULL AND k <> lower(k) AND k <> '_default'`},
		{"rate_limit_policies.model_credit_rates keys", `SELECT DISTINCT 'policy:' || id, k FROM rate_limit_policies, jsonb_object_keys(model_credit_rates) AS k WHERE model_credit_rates IS NOT NULL AND k <> lower(k) AND k <> '_default'`},
	}
	for _, q := range queries {
		rows, err := conn.Query(ctx, q.sql)
		if err != nil {
			fmt.Fprintf(os.Stderr, "preflight: %s: %v\n", q.label, err)
			return 1
		}
		for rows.Next() {
			var src, val string
			_ = rows.Scan(&src, &val)
			hits = append(hits, row{src, val})
		}
		rows.Close()
	}
	if len(hits) == 0 {
		return 0
	}
	fmt.Println("\n[3/5] Mixed-case model names found:")
	for _, h := range hits {
		fmt.Printf("  - %s: %q\n", h.source, h.value)
	}
	fmt.Println("      → Migration step 0 lowercases array entries (upstreams/api_keys).")
	fmt.Println("      → JSONB-key entries (plans/policies) are NOT auto-normalized; rewrite them manually.")
	return 1
}

func reportOrphanRateMapKeys(ctx context.Context, conn *pgx.Conn) int {
	rows, err := conn.Query(ctx, `
        WITH supported AS (
            SELECT DISTINCT lower(m) AS n
              FROM upstreams, unnest(supported_models) AS m
        ),
        rate_keys AS (
            SELECT DISTINCT lower(k) AS n
              FROM plans, jsonb_object_keys(model_credit_rates) AS k
              WHERE model_credit_rates IS NOT NULL
            UNION
            SELECT DISTINCT lower(k) AS n
              FROM rate_limit_policies, jsonb_object_keys(model_credit_rates) AS k
              WHERE model_credit_rates IS NOT NULL
        )
        SELECT n FROM rate_keys
         WHERE n <> '_default' AND n NOT IN (SELECT n FROM supported)
         ORDER BY n`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "preflight: query orphan rate-map keys: %v\n", err)
		return 1
	}
	defer rows.Close()

	var orphans []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		orphans = append(orphans, n)
	}
	if len(orphans) == 0 {
		return 0
	}
	fmt.Println("\n[4/5] Plan/policy rate-map keys NOT in any upstream's supported_models:")
	for _, n := range orphans {
		fmt.Printf("  - %s\n", n)
	}
	fmt.Println("      → These names will enter the catalog but be unroutable until an upstream supports them.")
	fmt.Println("      → Action (optional): clean up dead pricing entries before migration.")
	return 1
}

func reportUnseededRequestModels(ctx context.Context, conn *pgx.Conn, lookback time.Duration) int {
	since := time.Now().Add(-lookback)
	rows, err := conn.Query(ctx, `
        WITH seed AS (
            SELECT DISTINCT lower(unnest(supported_models)) AS n FROM upstreams
            UNION
            SELECT DISTINCT lower(jsonb_object_keys(model_credit_rates)) FROM plans WHERE model_credit_rates IS NOT NULL
            UNION
            SELECT DISTINCT lower(jsonb_object_keys(model_credit_rates)) FROM rate_limit_policies WHERE model_credit_rates IS NOT NULL
            UNION
            SELECT DISTINCT lower(unnest(allowed_models)) FROM api_keys WHERE allowed_models IS NOT NULL
        )
        SELECT lower(model) AS n, count(*)
          FROM requests
         WHERE created_at >= $1
           AND model <> ''
           AND lower(model) NOT IN (SELECT n FROM seed WHERE n <> '_default')
         GROUP BY lower(model)
         ORDER BY count(*) DESC
         LIMIT 50`, since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "preflight: query unseeded request models: %v\n", err)
		return 1
	}
	defer rows.Close()

	type hit struct {
		name  string
		count int64
	}
	var hits []hit
	for rows.Next() {
		var h hit
		_ = rows.Scan(&h.name, &h.count)
		hits = append(hits, h)
	}
	if len(hits) == 0 {
		return 0
	}
	fmt.Printf("\n[5/5] Models that clients sent in the last %s but are NOT in the catalog seed:\n", lookback)
	for _, h := range hits {
		fmt.Printf("  - %s   (%d requests)\n", h.name, h.count)
	}
	fmt.Println("      → After deploy these requests return 400 unsupported_model instead of being routed.")
	fmt.Println("      → Action: register each as an alias of the canonical name, OR add it to the catalog, OR accept the 400.")
	return 1
}
