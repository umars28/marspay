package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/testdb"
)

const (
	testPhone = "081200000001"
	testPin   = "294715"
)

func newStore(t *testing.T) (*Store, *pgxpool.Pool, context.Context, string) {
	t.Helper()

	pool, ctx := testdb.New(t)
	hash, err := HashPin(testPin)
	if err != nil {
		t.Fatalf("hash pin: %v", err)
	}

	userID := id.New("usr")
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, phone, full_name, pin_hash, kyc_tier, status)
		 VALUES ($1, $2, 'Test User', $3, 'verified', 'active')`,
		userID, testPhone, hash); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	return NewStore(pool), pool, ctx, userID
}

func login(t *testing.T, s *Store, ctx context.Context) *Tokens {
	t.Helper()

	challenge, err := s.StartOTP(ctx, testPhone)
	if err != nil {
		t.Fatalf("start otp: %v", err)
	}
	phone, err := s.VerifyOTP(ctx, challenge.ID, challenge.Code)
	if err != nil {
		t.Fatalf("verify otp: %v", err)
	}

	tokens, err := s.Login(ctx, LoginRequest{
		ChallengeID: challenge.ID, Phone: phone, Pin: testPin, Platform: "android",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return tokens
}

func TestTheSamePinHashesDifferentlyEveryTime(t *testing.T) {
	first, err := HashPin(testPin)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	second, err := HashPin(testPin)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	if first == second {
		t.Error("two hashes of the same PIN are identical, so the salt is not being used")
	}
	for _, h := range []string{first, second} {
		if strings.Contains(h, testPin) {
			t.Error("the PIN appears verbatim inside its own hash")
		}
		ok, err := CheckPin(testPin, h)
		if err != nil || !ok {
			t.Errorf("the correct PIN did not verify against its own hash: %v", err)
		}
	}

	ok, err := CheckPin("000000", first)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if ok {
		t.Error("a wrong PIN verified")
	}
}

func TestOnlySixDigitPinsAreAccepted(t *testing.T) {
	for _, bad := range []string{"", "12345", "1234567", "12345a", "abcdef", " 12345"} {
		if _, err := HashPin(bad); !errors.Is(err, ErrPinFormat) {
			t.Errorf("HashPin(%q) = %v, want ErrPinFormat", bad, err)
		}
	}
}

func TestACodeIsSpentByLoggingIn(t *testing.T) {
	s, _, ctx, _ := newStore(t)

	challenge, err := s.StartOTP(ctx, testPhone)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if challenge.Code == "" || len(challenge.Code) != 6 {
		t.Fatalf("code = %q, want six digits", challenge.Code)
	}

	phone, err := s.VerifyOTP(ctx, challenge.ID, challenge.Code)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := s.Login(ctx, LoginRequest{
		ChallengeID: challenge.ID, Phone: phone, Pin: testPin, Platform: "ios",
	}); err != nil {
		t.Fatalf("login: %v", err)
	}

	if _, err := s.Login(ctx, LoginRequest{
		ChallengeID: challenge.ID, Phone: phone, Pin: testPin, Platform: "ios",
	}); !errors.Is(err, ErrChallengeSpent) {
		t.Errorf("the same code logged in twice: %v", err)
	}
}

func TestAMistypedPinDoesNotBurnTheCode(t *testing.T) {
	s, _, ctx, _ := newStore(t)

	challenge, err := s.StartOTP(ctx, testPhone)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	phone, err := s.VerifyOTP(ctx, challenge.ID, challenge.Code)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if _, err := s.Login(ctx, LoginRequest{
		ChallengeID: challenge.ID, Phone: phone, Pin: "000000", Platform: "ios",
	}); !errors.Is(err, ErrWrongPin) {
		t.Fatalf("wrong pin: %v, want ErrWrongPin", err)
	}

	if _, err := s.Login(ctx, LoginRequest{
		ChallengeID: challenge.ID, Phone: phone, Pin: testPin, Platform: "ios",
	}); err != nil {
		t.Errorf("a typo cost the user their SMS: %v", err)
	}
}

func TestThreeWrongCodesBurnTheChallenge(t *testing.T) {
	s, _, ctx, _ := newStore(t)

	challenge, err := s.StartOTP(ctx, testPhone)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	for i := 0; i < 2; i++ {
		if _, err := s.VerifyOTP(ctx, challenge.ID, "000000"); !errors.Is(err, ErrWrongCode) {
			t.Fatalf("attempt %d: %v, want ErrWrongCode", i, err)
		}
	}
	if _, err := s.VerifyOTP(ctx, challenge.ID, "000000"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("third attempt: %v, want ErrTooManyAttempts", err)
	}

	if _, err := s.VerifyOTP(ctx, challenge.ID, challenge.Code); !errors.Is(err, ErrTooManyAttempts) {
		t.Error("the correct code still worked after the attempt limit was reached")
	}
}

func TestAnExpiredCodeIsRefused(t *testing.T) {
	s, _, ctx, _ := newStore(t)

	challenge, err := s.StartOTP(ctx, testPhone)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	s.now = func() time.Time { return time.Now().Add(OTPWindow + time.Minute) }
	if _, err := s.VerifyOTP(ctx, challenge.ID, challenge.Code); !errors.Is(err, ErrChallengeStale) {
		t.Errorf("got %v, want ErrChallengeStale", err)
	}
}

func TestThreeWrongPinsLockEntry(t *testing.T) {
	s, _, ctx, _ := newStore(t)

	for i := 0; i < MaxPinAttempts-1; i++ {
		_, err := s.Login(ctx, LoginRequest{Phone: testPhone, Pin: "000000", Platform: "ios"})
		if !errors.Is(err, ErrWrongPin) {
			t.Fatalf("attempt %d: %v, want ErrWrongPin", i, err)
		}
	}

	_, err := s.Login(ctx, LoginRequest{Phone: testPhone, Pin: "000000", Platform: "ios"})
	if !errors.Is(err, ErrPinLocked) {
		t.Fatalf("final attempt: %v, want ErrPinLocked", err)
	}

	_, err = s.Login(ctx, LoginRequest{Phone: testPhone, Pin: testPin, Platform: "ios"})
	if !errors.Is(err, ErrPinLocked) {
		t.Error("the correct PIN was accepted while entry was locked")
	}
}

func TestACorrectPinClearsEarlierFailures(t *testing.T) {
	s, pool, ctx, userID := newStore(t)

	if _, err := s.Login(ctx, LoginRequest{Phone: testPhone, Pin: "000000", Platform: "ios"}); !errors.Is(err, ErrWrongPin) {
		t.Fatalf("wrong pin: %v", err)
	}
	if _, err := s.Login(ctx, LoginRequest{Phone: testPhone, Pin: testPin, Platform: "ios"}); err != nil {
		t.Fatalf("correct pin: %v", err)
	}

	var attempts int
	if err := pool.QueryRow(ctx, `SELECT pin_attempts FROM users WHERE id = $1`, userID).
		Scan(&attempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	if attempts != 0 {
		t.Errorf("pin_attempts = %d after a successful login, want 0: "+
			"a counter that never resets locks out honest users", attempts)
	}
}

func TestNoTokenIsEverStoredInPlaintext(t *testing.T) {
	s, pool, ctx, _ := newStore(t)
	tokens := login(t, s, ctx)

	for _, secret := range []string{tokens.AccessToken, tokens.RefreshToken} {
		var found int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM sessions
			 WHERE access_hash = $1 OR refresh_hash = $1
			    OR access_hash LIKE '%' || $1 || '%'
			    OR refresh_hash LIKE '%' || $1 || '%'`, secret).Scan(&found); err != nil {
			t.Fatalf("scan sessions: %v", err)
		}
		if found != 0 {
			t.Errorf("a token was found verbatim in the sessions table")
		}
	}

	var accessHash string
	if err := pool.QueryRow(ctx,
		`SELECT access_hash FROM sessions WHERE id = $1`, tokens.SessionID).Scan(&accessHash); err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if accessHash != FastHash(tokens.AccessToken) {
		t.Error("the stored hash does not match the token it was derived from")
	}
}

func TestAnAccessTokenIdentifiesItsOwner(t *testing.T) {
	s, _, ctx, userID := newStore(t)
	tokens := login(t, s, ctx)

	p, err := s.Verify(ctx, tokens.AccessToken)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p.UserID != userID {
		t.Errorf("user = %q, want %q", p.UserID, userID)
	}
	if p.DeviceID != tokens.DeviceID || p.SessionID != tokens.SessionID {
		t.Errorf("principal %+v does not match the issued tokens", p)
	}

	if _, err := s.Verify(ctx, "mp_at_not-a-real-token"); !errors.Is(err, ErrNoSession) {
		t.Errorf("an invented token gave %v, want ErrNoSession", err)
	}
}

func TestAnExpiredAccessTokenIsRefused(t *testing.T) {
	s, _, ctx, _ := newStore(t)
	tokens := login(t, s, ctx)

	s.now = func() time.Time { return time.Now().Add(AccessTTL + time.Minute) }
	if _, err := s.Verify(ctx, tokens.AccessToken); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("got %v, want ErrSessionExpired", err)
	}
}

func TestRefreshingRetiresTheOldTokens(t *testing.T) {
	s, _, ctx, _ := newStore(t)
	first := login(t, s, ctx)

	second, err := s.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh returned the same tokens; rotation did not happen")
	}
	if second.DeviceID != first.DeviceID {
		t.Errorf("device changed on refresh: %q then %q", first.DeviceID, second.DeviceID)
	}

	if _, err := s.Verify(ctx, first.AccessToken); !errors.Is(err, ErrNoSession) {
		t.Errorf("the old access token still works: %v", err)
	}
	if _, err := s.Verify(ctx, second.AccessToken); err != nil {
		t.Errorf("the new access token does not work: %v", err)
	}
}

func TestReplayingARotatedRefreshTokenKillsTheWholeChain(t *testing.T) {
	s, _, ctx, _ := newStore(t)
	first := login(t, s, ctx)

	second, err := s.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	third, err := s.Refresh(ctx, second.RefreshToken)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	if _, err := s.Refresh(ctx, first.RefreshToken); !errors.Is(err, ErrTokenReplayed) {
		t.Fatalf("replay gave %v, want ErrTokenReplayed", err)
	}

	if _, err := s.Verify(ctx, third.AccessToken); !errors.Is(err, ErrNoSession) {
		t.Error("the live session survived a replayed refresh token; " +
			"a stolen token would keep working alongside the real client")
	}
	if _, err := s.Refresh(ctx, third.RefreshToken); err == nil {
		t.Error("the newest refresh token still works after a replay was detected")
	}
}

func TestTheSameDeviceIsNotRegisteredTwice(t *testing.T) {
	s, _, ctx, userID := newStore(t)

	first := login(t, s, ctx)
	if !first.NewDevice {
		t.Error("the first login did not report a new device")
	}

	second, err := s.Login(ctx, LoginRequest{
		Phone: testPhone, Pin: testPin, Platform: "android", DeviceID: first.DeviceID,
	})
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if second.NewDevice {
		t.Error("a returning device was reported as new")
	}
	if second.DeviceID != first.DeviceID {
		t.Errorf("device id changed: %q then %q", first.DeviceID, second.DeviceID)
	}

	devices, err := s.Devices(ctx, userID)
	if err != nil {
		t.Fatalf("devices: %v", err)
	}
	if len(devices) != 1 {
		t.Errorf("%d devices recorded for two logins from one handset", len(devices))
	}
}

func TestRevokingADeviceSignsOutItsSessions(t *testing.T) {
	s, _, ctx, userID := newStore(t)

	phone := login(t, s, ctx)
	laptop, err := s.Login(ctx, LoginRequest{Phone: testPhone, Pin: testPin, Platform: "web"})
	if err != nil {
		t.Fatalf("second device: %v", err)
	}

	if err := s.RevokeDevice(ctx, userID, phone.DeviceID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if _, err := s.Verify(ctx, phone.AccessToken); !errors.Is(err, ErrNoSession) {
		t.Error("the revoked device's token still works")
	}
	if _, err := s.Verify(ctx, laptop.AccessToken); err != nil {
		t.Errorf("revoking one device signed out another: %v", err)
	}
	if _, err := s.Refresh(ctx, phone.RefreshToken); err == nil {
		t.Error("the revoked device can still refresh its way back in")
	}
}

func TestLoggingOutEndsOnlyThatSession(t *testing.T) {
	s, _, ctx, _ := newStore(t)

	phone := login(t, s, ctx)
	laptop, err := s.Login(ctx, LoginRequest{Phone: testPhone, Pin: testPin, Platform: "web"})
	if err != nil {
		t.Fatalf("second device: %v", err)
	}

	if err := s.Logout(ctx, phone.SessionID); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := s.Verify(ctx, phone.AccessToken); !errors.Is(err, ErrNoSession) {
		t.Error("the token still works after logging out")
	}
	if _, err := s.Verify(ctx, laptop.AccessToken); err != nil {
		t.Errorf("logging out one session ended another: %v", err)
	}
}

func TestABlockedAccountCannotUseAValidToken(t *testing.T) {
	s, pool, ctx, userID := newStore(t)
	tokens := login(t, s, ctx)

	if _, err := pool.Exec(ctx, `UPDATE users SET status = 'blocked' WHERE id = $1`, userID); err != nil {
		t.Fatalf("block user: %v", err)
	}

	if _, err := s.Verify(ctx, tokens.AccessToken); !errors.Is(err, ErrNotActive) {
		t.Errorf("got %v, want ErrNotActive: blocking an account must take effect immediately", err)
	}
}
