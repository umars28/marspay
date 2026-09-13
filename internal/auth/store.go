package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
)

const (
	OTPWindow      = 5 * time.Minute
	AccessTTL      = 15 * time.Minute
	RefreshTTL     = 30 * 24 * time.Hour
	MaxPinAttempts = 3
	PinLockout     = 15 * time.Minute
)

var (
	ErrNoChallenge     = errors.New("auth: no such challenge")
	ErrChallengeSpent  = errors.New("auth: challenge already used")
	ErrChallengeStale  = errors.New("auth: challenge expired")
	ErrWrongCode       = errors.New("auth: wrong code")
	ErrTooManyAttempts = errors.New("auth: too many attempts")
	ErrNoUser          = errors.New("auth: no such user")
	ErrWrongPin        = errors.New("auth: wrong PIN")
	ErrPinLocked       = errors.New("auth: PIN entry is locked")
	ErrNotActive       = errors.New("auth: account is not active")
	ErrNoSession       = errors.New("auth: no such session")
	ErrSessionExpired  = errors.New("auth: session expired")
	ErrTokenReplayed   = errors.New("auth: refresh token was already rotated")
)

type Cache interface {
	Get(ctx context.Context, key string) (string, bool)
	Set(ctx context.Context, key, value string, ttl time.Duration)
	Del(ctx context.Context, key string)
}

type Device struct {
	ID         string     `json:"id"`
	Platform   string     `json:"platform"`
	Model      string     `json:"model,omitempty"`
	Trusted    bool       `json:"trusted"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Tokens struct {
	SessionID        string    `json:"session_id"`
	DeviceID         string    `json:"device_id"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	NewDevice        bool      `json:"new_device"`
}

type Principal struct {
	UserID    string
	DeviceID  string
	SessionID string
	Role      string
}

type Store struct {
	pool  *pgxpool.Pool
	cache Cache
	now   func() time.Time
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, now: time.Now}
}

func (s *Store) WithCache(c Cache) *Store {
	s.cache = c
	return s
}

type Challenge struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
	Code      string    `json:"code,omitempty"`
}

func (s *Store) StartOTP(ctx context.Context, phone string) (Challenge, error) {
	code, err := NewOTP()
	if err != nil {
		return Challenge{}, err
	}

	c := Challenge{
		ID:        id.New("otp"),
		ExpiresAt: s.now().Add(OTPWindow),
		Code:      code,
	}

	_, err = s.pool.Exec(ctx,
		`INSERT INTO otp_challenges (id, phone, code_hash, expires_at)
		 VALUES ($1, $2, $3, $4)`,
		c.ID, phone, FastHash(code), c.ExpiresAt)
	if err != nil {
		return Challenge{}, fmt.Errorf("auth: insert challenge: %w", err)
	}
	return c, nil
}

func (s *Store) VerifyOTP(ctx context.Context, challengeID, code string) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		phone      string
		codeHash   string
		attempts   int
		maxAttempt int
		expiresAt  time.Time
		consumedAt *time.Time
	)
	err = tx.QueryRow(ctx,
		`SELECT phone, code_hash, attempts, max_attempts, expires_at, consumed_at
		 FROM otp_challenges WHERE id = $1 FOR UPDATE`, challengeID).
		Scan(&phone, &codeHash, &attempts, &maxAttempt, &expiresAt, &consumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoChallenge
	}
	if err != nil {
		return "", err
	}

	switch {
	case consumedAt != nil:
		return "", ErrChallengeSpent
	case s.now().After(expiresAt):
		return "", ErrChallengeStale
	case attempts >= maxAttempt:
		return "", ErrTooManyAttempts
	}

	if FastHash(code) != codeHash {
		if _, err := tx.Exec(ctx,
			`UPDATE otp_challenges SET attempts = attempts + 1 WHERE id = $1`, challengeID); err != nil {
			return "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		if attempts+1 >= maxAttempt {
			return "", ErrTooManyAttempts
		}
		return "", ErrWrongCode
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return phone, nil
}

func consumeChallenge(ctx context.Context, tx pgx.Tx, challengeID string, now time.Time) error {
	var consumedAt *time.Time
	var expiresAt time.Time
	err := tx.QueryRow(ctx,
		`SELECT consumed_at, expires_at FROM otp_challenges WHERE id = $1 FOR UPDATE`,
		challengeID).Scan(&consumedAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoChallenge
	}
	if err != nil {
		return err
	}

	switch {
	case consumedAt != nil:
		return ErrChallengeSpent
	case now.After(expiresAt):
		return ErrChallengeStale
	}

	_, err = tx.Exec(ctx,
		`UPDATE otp_challenges SET consumed_at = $2 WHERE id = $1`, challengeID, now)
	return err
}

type LoginRequest struct {
	ChallengeID string
	Phone       string
	Pin         string
	Platform    string
	Model       string
	DeviceID    string
}

func (s *Store) Login(ctx context.Context, req LoginRequest) (*Tokens, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		userID      string
		pinHash     string
		status      string
		attempts    int
		lockedUntil *time.Time
	)
	err = tx.QueryRow(ctx,
		`SELECT id, pin_hash, status, pin_attempts, pin_locked_until
		 FROM users WHERE phone = $1 FOR UPDATE`, req.Phone).
		Scan(&userID, &pinHash, &status, &attempts, &lockedUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoUser
	}
	if err != nil {
		return nil, err
	}

	if status != "active" {
		return nil, ErrNotActive
	}
	if lockedUntil != nil && s.now().Before(*lockedUntil) {
		return nil, ErrPinLocked
	}

	ok, err := CheckPin(req.Pin, pinHash)
	if err != nil {
		return nil, err
	}
	if !ok {
		attempts++
		var until *time.Time
		if attempts >= MaxPinAttempts {
			t := s.now().Add(PinLockout)
			until = &t
			attempts = 0
		}
		if _, err := tx.Exec(ctx,
			`UPDATE users SET pin_attempts = $2, pin_locked_until = $3 WHERE id = $1`,
			userID, attempts, until); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		if until != nil {
			return nil, ErrPinLocked
		}
		return nil, ErrWrongPin
	}

	if _, err := tx.Exec(ctx,
		`UPDATE users SET pin_attempts = 0, pin_locked_until = NULL WHERE id = $1`, userID); err != nil {
		return nil, err
	}

	if req.ChallengeID != "" {
		if err := consumeChallenge(ctx, tx, req.ChallengeID, s.now()); err != nil {
			return nil, err
		}
	}

	deviceID, isNew, err := s.upsertDevice(ctx, tx, userID, req)
	if err != nil {
		return nil, err
	}

	tokens, err := s.issue(ctx, tx, userID, deviceID)
	if err != nil {
		return nil, err
	}
	tokens.NewDevice = isNew

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return tokens, nil
}

func (s *Store) upsertDevice(ctx context.Context, tx pgx.Tx, userID string, req LoginRequest) (string, bool, error) {
	if req.DeviceID != "" {
		var owner string
		var revoked *time.Time
		err := tx.QueryRow(ctx,
			`SELECT user_id, revoked_at FROM devices WHERE id = $1`, req.DeviceID).Scan(&owner, &revoked)
		if err == nil && owner == userID && revoked == nil {
			if _, err := tx.Exec(ctx,
				`UPDATE devices SET last_seen_at = $2 WHERE id = $1`, req.DeviceID, s.now()); err != nil {
				return "", false, err
			}
			return req.DeviceID, false, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", false, err
		}
	}

	deviceID := id.New("dev")
	if _, err := tx.Exec(ctx,
		`INSERT INTO devices (id, user_id, platform, model, last_seen_at)
		 VALUES ($1, $2, $3, NULLIF($4, ''), $5)`,
		deviceID, userID, req.Platform, req.Model, s.now()); err != nil {
		return "", false, fmt.Errorf("auth: insert device: %w", err)
	}
	return deviceID, true, nil
}

func (s *Store) issue(ctx context.Context, tx pgx.Tx, userID, deviceID string) (*Tokens, error) {
	access, accessHash, err := NewToken("mp_at_")
	if err != nil {
		return nil, err
	}
	refresh, refreshHash, err := NewToken("mp_rt_")
	if err != nil {
		return nil, err
	}

	now := s.now()
	t := &Tokens{
		SessionID:        id.New("sess"),
		DeviceID:         deviceID,
		AccessToken:      access,
		RefreshToken:     refresh,
		AccessExpiresAt:  now.Add(AccessTTL),
		RefreshExpiresAt: now.Add(RefreshTTL),
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO sessions
		   (id, user_id, device_id, access_hash, refresh_hash,
		    access_expires_at, refresh_expires_at, last_seen_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		t.SessionID, userID, deviceID, accessHash, refreshHash,
		t.AccessExpiresAt, t.RefreshExpiresAt, now)
	if err != nil {
		return nil, fmt.Errorf("auth: insert session: %w", err)
	}
	return t, nil
}

func (s *Store) Verify(ctx context.Context, accessToken string) (*Principal, error) {
	hash := FastHash(accessToken)
	key := "auth:session:" + hash

	if s.cache != nil {
		if v, ok := s.cache.Get(ctx, key); ok {
			p, err := decodePrincipal(v)
			if err == nil {
				return p, nil
			}
		}
	}

	var (
		p         Principal
		expiresAt time.Time
		revokedAt *time.Time
		status    string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT s.id, s.user_id, s.device_id, s.access_expires_at, s.revoked_at, u.status, u.role
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.access_hash = $1`, hash).
		Scan(&p.SessionID, &p.UserID, &p.DeviceID, &expiresAt, &revokedAt, &status, &p.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, err
	}

	switch {
	case revokedAt != nil:
		return nil, ErrNoSession
	case s.now().After(expiresAt):
		return nil, ErrSessionExpired
	case status != "active":
		return nil, ErrNotActive
	}

	if s.cache != nil {
		ttl := time.Until(expiresAt)
		if ttl > SessionCacheTTL {
			ttl = SessionCacheTTL
		}
		if ttl > 0 {
			s.cache.Set(ctx, key, encodePrincipal(&p), ttl)
		}
	}
	return &p, nil
}

const SessionCacheTTL = 60 * time.Second

func (s *Store) Refresh(ctx context.Context, refreshToken string) (*Tokens, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		sessionID  string
		userID     string
		deviceID   string
		accessHash string
		expiresAt  time.Time
		revokedAt  *time.Time
		rotatedTo  *string
	)
	err = tx.QueryRow(ctx,
		`SELECT id, user_id, device_id, access_hash, refresh_expires_at, revoked_at, rotated_to
		 FROM sessions WHERE refresh_hash = $1 FOR UPDATE`, FastHash(refreshToken)).
		Scan(&sessionID, &userID, &deviceID, &accessHash, &expiresAt, &revokedAt, &rotatedTo)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, err
	}

	if rotatedTo != nil {
		if err := revokeChain(ctx, tx, sessionID, "refresh token replayed"); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		s.forget(ctx, accessHash)
		return nil, ErrTokenReplayed
	}
	if revokedAt != nil {
		return nil, ErrNoSession
	}
	if s.now().After(expiresAt) {
		return nil, ErrSessionExpired
	}

	next, err := s.issue(ctx, tx, userID, deviceID)
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE sessions SET rotated_to = $2, revoked_at = $3, revoked_reason = 'rotated'
		 WHERE id = $1`, sessionID, next.SessionID, s.now()); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE devices SET last_seen_at = $2 WHERE id = $1`, deviceID, s.now()); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.forget(ctx, accessHash)
	return next, nil
}

func revokeChain(ctx context.Context, tx pgx.Tx, sessionID, reason string) error {
	_, err := tx.Exec(ctx,
		`WITH RECURSIVE chain AS (
		   SELECT id, rotated_to FROM sessions WHERE id = $1
		   UNION ALL
		   SELECT s.id, s.rotated_to FROM sessions s JOIN chain c ON s.id = c.rotated_to
		 )
		 UPDATE sessions SET revoked_at = now(), revoked_reason = $2
		 WHERE id IN (SELECT id FROM chain) AND revoked_reason IS DISTINCT FROM $2`,
		sessionID, reason)
	return err
}

func (s *Store) Logout(ctx context.Context, sessionID string) error {
	var accessHash string
	err := s.pool.QueryRow(ctx,
		`UPDATE sessions SET revoked_at = now(), revoked_reason = 'logout'
		 WHERE id = $1 AND revoked_at IS NULL
		 RETURNING access_hash`, sessionID).Scan(&accessHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoSession
	}
	if err != nil {
		return err
	}
	s.forget(ctx, accessHash)
	return nil
}

func (s *Store) Devices(ctx context.Context, userID string) ([]Device, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, platform, COALESCE(model, ''), trusted, last_seen_at, revoked_at, created_at
		 FROM devices WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	devices := []Device{}
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.Platform, &d.Model, &d.Trusted,
			&d.LastSeenAt, &d.RevokedAt, &d.CreatedAt); err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

func (s *Store) RevokeDevice(ctx context.Context, userID, deviceID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE devices SET revoked_at = now()
		 WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, deviceID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoSession
	}

	rows, err := tx.Query(ctx,
		`UPDATE sessions SET revoked_at = now(), revoked_reason = 'device revoked'
		 WHERE device_id = $1 AND revoked_at IS NULL
		 RETURNING access_hash`, deviceID)
	if err != nil {
		return err
	}

	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return err
		}
		hashes = append(hashes, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, h := range hashes {
		s.forget(ctx, h)
	}
	return nil
}

func (s *Store) forget(ctx context.Context, accessHash string) {
	if s.cache != nil {
		s.cache.Del(ctx, "auth:session:"+accessHash)
	}
}

func encodePrincipal(p *Principal) string {
	return p.UserID + "|" + p.DeviceID + "|" + p.SessionID + "|" + p.Role
}

func decodePrincipal(v string) (*Principal, error) {
	var p Principal
	n := 0
	start := 0
	for i := 0; i <= len(v); i++ {
		if i == len(v) || v[i] == '|' {
			switch n {
			case 0:
				p.UserID = v[start:i]
			case 1:
				p.DeviceID = v[start:i]
			case 2:
				p.SessionID = v[start:i]
			case 3:
				p.Role = v[start:i]
			}
			n++
			start = i + 1
		}
	}
	if n != 4 || p.UserID == "" {
		return nil, errors.New("auth: malformed cached principal")
	}
	return &p, nil
}
