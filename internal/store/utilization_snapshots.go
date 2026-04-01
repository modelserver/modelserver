package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// UtilizationSnapshot holds a single data point for rate inference.
type UtilizationSnapshot struct {
	ID             string                          `json:"id"`
	UpstreamID     string                          `json:"upstream_id"`
	WindowType     string                          `json:"window_type"`
	OfficialPct    float64                         `json:"official_pct"`
	ResetsAt       *time.Time                      `json:"resets_at"`
	ModelBreakdown map[string]*UpstreamTokenBreakdown `json:"model_breakdown"`
	TotalCredits   float64                         `json:"total_credits"`
	CreatedAt      time.Time                       `json:"created_at"`
}

// CreateUtilizationSnapshot inserts a new snapshot. Duplicate (upstream_id, window_type, resets_at) is silently ignored.
func (s *Store) CreateUtilizationSnapshot(snap *UtilizationSnapshot) error {
	breakdown, err := json.Marshal(snap.ModelBreakdown)
	if err != nil {
		return fmt.Errorf("marshal model_breakdown: %w", err)
	}
	_, err = s.pool.Exec(context.Background(), `
		INSERT INTO utilization_snapshots (upstream_id, window_type, official_pct, resets_at, model_breakdown, total_credits)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (upstream_id, window_type, resets_at) DO UPDATE SET
			official_pct = EXCLUDED.official_pct,
			model_breakdown = EXCLUDED.model_breakdown,
			total_credits = EXCLUDED.total_credits`,
		snap.UpstreamID, snap.WindowType, snap.OfficialPct, snap.ResetsAt, breakdown, snap.TotalCredits)
	return err
}

// ListUtilizationSnapshots returns snapshots for an upstream, newest first.
func (s *Store) ListUtilizationSnapshots(upstreamID, windowType string, limit int) ([]UtilizationSnapshot, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, upstream_id, window_type, official_pct, resets_at, model_breakdown, total_credits, created_at
		FROM utilization_snapshots
		WHERE upstream_id = $1 AND window_type = $2
		ORDER BY created_at DESC
		LIMIT $3`, upstreamID, windowType, limit)
	if err != nil {
		return nil, fmt.Errorf("list utilization snapshots: %w", err)
	}
	defer rows.Close()

	var snaps []UtilizationSnapshot
	for rows.Next() {
		var snap UtilizationSnapshot
		var breakdownJSON []byte
		if err := rows.Scan(&snap.ID, &snap.UpstreamID, &snap.WindowType, &snap.OfficialPct,
			&snap.ResetsAt, &breakdownJSON, &snap.TotalCredits, &snap.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(breakdownJSON, &snap.ModelBreakdown); err != nil {
			return nil, fmt.Errorf("unmarshal model_breakdown: %w", err)
		}
		snaps = append(snaps, snap)
	}
	return snaps, rows.Err()
}
