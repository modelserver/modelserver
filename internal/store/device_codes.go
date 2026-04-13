package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// DeviceCode represents a row in the device_codes table.
type DeviceCode struct {
	ID                string
	DeviceCode        string
	UserCode          string
	ClientID          string
	Scopes            []string
	Status            string // pending, approved, denied, expired
	VerificationNonce string
	AccessToken       []byte // encrypted
	RefreshToken      []byte // encrypted
	TokenType         string
	TokenExpiresIn    int
	ExpiresAt         time.Time
	PollInterval      int
	LastPolledAt      *time.Time
	CreatedAt         time.Time
}

// CreateDeviceCode inserts a new device code record.
func (s *Store) CreateDeviceCode(ctx context.Context, dc *DeviceCode) error {
	return s.pool.QueryRow(ctx, `
		INSERT INTO device_codes (device_code, user_code, client_id, scopes, verification_nonce, expires_at, poll_interval)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at`,
		dc.DeviceCode, dc.UserCode, dc.ClientID, dc.Scopes, dc.VerificationNonce, dc.ExpiresAt, dc.PollInterval,
	).Scan(&dc.ID, &dc.CreatedAt)
}

// GetDeviceCodeByUserCode returns the pending device code matching the user code.
// Returns nil, nil if not found or not pending.
func (s *Store) GetDeviceCodeByUserCode(ctx context.Context, userCode string) (*DeviceCode, error) {
	dc := &DeviceCode{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, device_code, user_code, client_id, scopes, status,
			verification_nonce, expires_at, poll_interval, created_at
		FROM device_codes
		WHERE user_code = $1 AND status = 'pending' AND expires_at > NOW()`,
		userCode,
	).Scan(&dc.ID, &dc.DeviceCode, &dc.UserCode, &dc.ClientID, &dc.Scopes, &dc.Status,
		&dc.VerificationNonce, &dc.ExpiresAt, &dc.PollInterval, &dc.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device code by user code: %w", err)
	}
	return dc, nil
}

// GetDeviceCodeByNonce returns the device code matching the verification nonce.
// Returns nil, nil if not found.
func (s *Store) GetDeviceCodeByNonce(ctx context.Context, nonce string) (*DeviceCode, error) {
	dc := &DeviceCode{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, device_code, user_code, client_id, scopes, status,
			verification_nonce, expires_at, poll_interval, created_at
		FROM device_codes
		WHERE verification_nonce = $1`,
		nonce,
	).Scan(&dc.ID, &dc.DeviceCode, &dc.UserCode, &dc.ClientID, &dc.Scopes, &dc.Status,
		&dc.VerificationNonce, &dc.ExpiresAt, &dc.PollInterval, &dc.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device code by nonce: %w", err)
	}
	return dc, nil
}

// GetDeviceCodeByCode returns the device code matching the device code string.
// Returns nil, nil if not found.
func (s *Store) GetDeviceCodeByCode(ctx context.Context, deviceCode string) (*DeviceCode, error) {
	dc := &DeviceCode{}
	var lastPolledAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT id, device_code, user_code, client_id, scopes, status,
			verification_nonce, access_token, refresh_token, token_type, token_expires_in,
			expires_at, poll_interval, last_polled_at, created_at
		FROM device_codes
		WHERE device_code = $1`,
		deviceCode,
	).Scan(&dc.ID, &dc.DeviceCode, &dc.UserCode, &dc.ClientID, &dc.Scopes, &dc.Status,
		&dc.VerificationNonce, &dc.AccessToken, &dc.RefreshToken, &dc.TokenType, &dc.TokenExpiresIn,
		&dc.ExpiresAt, &dc.PollInterval, &lastPolledAt, &dc.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device code by code: %w", err)
	}
	dc.LastPolledAt = lastPolledAt
	return dc, nil
}

// ApproveDeviceCode sets the device code status to approved and stores the encrypted tokens.
func (s *Store) ApproveDeviceCode(ctx context.Context, id string, accessToken, refreshToken []byte, tokenType string, tokenExpiresIn int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE device_codes
		SET status = 'approved', access_token = $2, refresh_token = $3,
			token_type = $4, token_expires_in = $5
		WHERE id = $1`,
		id, accessToken, refreshToken, tokenType, tokenExpiresIn)
	return err
}

// DenyDeviceCode sets the device code status to denied.
func (s *Store) DenyDeviceCode(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE device_codes SET status = 'denied' WHERE id = $1`, id)
	return err
}

// UpdateDeviceCodePoll updates last_polled_at and optionally increments poll_interval (for slow_down).
func (s *Store) UpdateDeviceCodePoll(ctx context.Context, id string, slowDown bool) error {
	if slowDown {
		_, err := s.pool.Exec(ctx, `
			UPDATE device_codes SET last_polled_at = NOW(), poll_interval = poll_interval + 5 WHERE id = $1`, id)
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE device_codes SET last_polled_at = NOW() WHERE id = $1`, id)
	return err
}

// DeleteDeviceCode removes a device code record by ID.
func (s *Store) DeleteDeviceCode(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM device_codes WHERE id = $1`, id)
	return err
}

// DeleteExpiredDeviceCodes removes all device codes that have expired.
func (s *Store) DeleteExpiredDeviceCodes(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM device_codes WHERE expires_at < NOW()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
