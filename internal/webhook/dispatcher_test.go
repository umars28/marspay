package webhook

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/testdb"
)

const endpointSecret = "whsec_integration_3f9a"

type receiver struct {
	server   *httptest.Server
	hits     atomic.Int64
	failFor  int64
	bodies   []string
	headers  []string
	mu       sync.Mutex
	delay    time.Duration
	statuses []int
}

func newReceiver(t *testing.T, failFor int64) *receiver {
	t.Helper()
	r := &receiver{failFor: failFor}

	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		n := r.hits.Add(1)
		body, _ := io.ReadAll(req.Body)

		r.mu.Lock()
		r.bodies = append(r.bodies, string(body))
		r.headers = append(r.headers, req.Header.Get(Header))
		r.mu.Unlock()

		if r.delay > 0 {
			time.Sleep(r.delay)
		}
		if n <= r.failFor {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *receiver) snapshot() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...), append([]string(nil), r.headers...)
}

type harness struct {
	pool       *pgxpool.Pool
	dispatcher *Dispatcher
	merchantID string
	endpointID string
	clock      time.Time
}

func newHarness(t *testing.T, url string) (*harness, context.Context) {
	t.Helper()

	pool, ctx := testdb.New(t)
	merchantID := id.New("merch")
	endpointID := id.New("wep")

	_, err := pool.Exec(ctx,
		`INSERT INTO merchants (id, legal_name, display_name, category, status)
		 VALUES ($1, 'PT Hook', 'Hook Merchant', 'food', 'active')`, merchantID)
	if err != nil {
		t.Fatalf("seed merchant: %v", err)
	}

	_, err = pool.Exec(ctx,
		`INSERT INTO webhook_endpoints (id, merchant_id, url, signing_secret, events, timeout_ms)
		 VALUES ($1, $2, $3, $4, ARRAY['payment.succeeded'], 2000)`,
		endpointID, merchantID, url, endpointSecret)
	if err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}

	h := &harness{
		pool:       pool,
		dispatcher: NewDispatcher(pool),
		merchantID: merchantID,
		endpointID: endpointID,
		clock:      time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC),
	}
	return h, ctx
}

func (h *harness) enqueue(t *testing.T, ctx context.Context, payload string) string {
	t.Helper()
	eventID, err := h.dispatcher.Enqueue(ctx, Event{
		MerchantID: h.merchantID,
		Type:       "payment.succeeded",
		ResourceID: id.New("pay"),
		Payload:    []byte(payload),
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return eventID
}

func (h *harness) drain(t *testing.T, ctx context.Context, rounds int) []Attempt {
	t.Helper()
	var all []Attempt

	for i := 0; i < rounds; i++ {
		if _, err := h.pool.Exec(ctx,
			`UPDATE webhook_deliveries SET next_retry_at = now()
			 WHERE status IN ($1, $2)`, StatusPending, StatusRetrying); err != nil {
			t.Fatalf("advance clock: %v", err)
		}

		attempts, err := h.dispatcher.DispatchBatch(ctx, 10)
		if err != nil {
			t.Fatalf("dispatch round %d: %v", i, err)
		}
		all = append(all, attempts...)
		if len(attempts) == 0 {
			break
		}
	}
	return all
}

func (h *harness) status(t *testing.T, ctx context.Context) (string, int) {
	t.Helper()
	var status string
	var attempt int
	err := h.pool.QueryRow(ctx,
		`SELECT status, attempt FROM webhook_deliveries LIMIT 1`).Scan(&status, &attempt)
	if err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	return status, attempt
}

func TestSuccessfulDeliveryIsSignedAndMarkedDelivered(t *testing.T) {
	rec := newReceiver(t, 0)
	h, ctx := newHarness(t, rec.server.URL)

	payload := `{"id":"evt_1","type":"payment.succeeded","data":{"amount":3200000}}`
	h.enqueue(t, ctx, payload)

	attempts := h.drain(t, ctx, 1)
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].Status != StatusDelivered {
		t.Errorf("status = %q, want delivered (err %v)", attempts[0].Status, attempts[0].Err)
	}

	bodies, headers := rec.snapshot()
	if len(bodies) != 1 || bodies[0] != payload {
		t.Fatalf("receiver got %q, want the exact payload", bodies)
	}
	if err := Verify(endpointSecret, headers[0], []byte(bodies[0]), time.Now()); err != nil {
		t.Errorf("receiver could not verify the signature: %v", err)
	}

	status, attempt := h.status(t, ctx)
	if status != StatusDelivered || attempt != 1 {
		t.Errorf("stored status=%q attempt=%d, want delivered and 1", status, attempt)
	}
}

func TestTransientFailuresAreRetriedUntilTheySucceed(t *testing.T) {
	rec := newReceiver(t, 3)
	h, ctx := newHarness(t, rec.server.URL)
	h.enqueue(t, ctx, `{"id":"evt_2"}`)

	attempts := h.drain(t, ctx, 6)

	if len(attempts) != 4 {
		t.Fatalf("attempts = %d, want 4 (three failures then a success)", len(attempts))
	}
	for i := 0; i < 3; i++ {
		if attempts[i].Status != StatusRetrying {
			t.Errorf("attempt %d status = %q, want retrying", i+1, attempts[i].Status)
		}
		if attempts[i].ResponseCode != http.StatusInternalServerError {
			t.Errorf("attempt %d code = %d, want 500", i+1, attempts[i].ResponseCode)
		}
	}
	if attempts[3].Status != StatusDelivered {
		t.Errorf("final status = %q, want delivered", attempts[3].Status)
	}

	status, attempt := h.status(t, ctx)
	if status != StatusDelivered || attempt != 4 {
		t.Errorf("stored status=%q attempt=%d, want delivered and 4", status, attempt)
	}
}

func TestAnEndpointThatNeverRecoversLandsInTheDeadLetterQueue(t *testing.T) {
	rec := newReceiver(t, 1_000)
	h, ctx := newHarness(t, rec.server.URL)
	h.enqueue(t, ctx, `{"id":"evt_3"}`)

	attempts := h.drain(t, ctx, MaxAttempts()+3)

	if len(attempts) != MaxAttempts() {
		t.Fatalf("attempts = %d, want exactly %d", len(attempts), MaxAttempts())
	}
	if last := attempts[len(attempts)-1]; last.Status != StatusDeadLetter {
		t.Errorf("final status = %q, want dead_letter", last.Status)
	}

	dead, err := h.dispatcher.DeadLetters(ctx)
	if err != nil {
		t.Fatalf("dead letters: %v", err)
	}
	if len(dead) != 1 {
		t.Fatalf("dead letters = %d, want 1", len(dead))
	}
	if dead[0].Attempt != MaxAttempts() {
		t.Errorf("dead letter recorded %d attempts, want %d", dead[0].Attempt, MaxAttempts())
	}

	if n := rec.hits.Load(); n != int64(MaxAttempts()) {
		t.Errorf("endpoint was called %d times, want %d: nothing should keep hammering it",
			n, MaxAttempts())
	}
}

func TestReplayTakesADeadLetterOutOfTheQueue(t *testing.T) {
	rec := newReceiver(t, 1_000)
	h, ctx := newHarness(t, rec.server.URL)
	h.enqueue(t, ctx, `{"id":"evt_4"}`)
	h.drain(t, ctx, MaxAttempts()+2)

	dead, err := h.dispatcher.DeadLetters(ctx)
	if err != nil || len(dead) != 1 {
		t.Fatalf("expected one dead letter, got %d (%v)", len(dead), err)
	}

	rec.failFor = 0
	if err := h.dispatcher.Replay(ctx, dead[0].DeliveryID, "umar@marspay"); err != nil {
		t.Fatalf("replay: %v", err)
	}

	attempts := h.drain(t, ctx, 2)
	if len(attempts) == 0 || attempts[0].Status != StatusDelivered {
		t.Fatalf("after replay the delivery did not succeed: %+v", attempts)
	}

	dead, err = h.dispatcher.DeadLetters(ctx)
	if err != nil {
		t.Fatalf("dead letters: %v", err)
	}
	if len(dead) != 0 {
		t.Errorf("dead letter queue still has %d rows", len(dead))
	}
}

func TestReplayRefusesAnythingNotInTheDeadLetterQueue(t *testing.T) {
	rec := newReceiver(t, 0)
	h, ctx := newHarness(t, rec.server.URL)
	h.enqueue(t, ctx, `{"id":"evt_5"}`)
	h.drain(t, ctx, 1)

	var deliveryID string
	if err := h.pool.QueryRow(ctx, `SELECT id FROM webhook_deliveries LIMIT 1`).Scan(&deliveryID); err != nil {
		t.Fatalf("read delivery: %v", err)
	}

	if err := h.dispatcher.Replay(ctx, deliveryID, "umar@marspay"); err == nil {
		t.Error("replaying a delivered webhook was accepted")
	}
	if err := h.dispatcher.Replay(ctx, deliveryID, ""); err == nil {
		t.Error("replaying without an actor was accepted")
	}
}

func TestConcurrentDispatchersNeverDoubleDeliver(t *testing.T) {
	rec := newReceiver(t, 0)
	rec.delay = 40 * time.Millisecond
	h, ctx := newHarness(t, rec.server.URL)

	const events = 12
	for i := 0; i < events; i++ {
		h.enqueue(t, ctx, `{"id":"evt_concurrent"}`)
	}

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				if _, err := h.dispatcher.DispatchBatch(context.Background(), 5); err != nil {
					t.Errorf("dispatch: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if n := rec.hits.Load(); n != events {
		t.Errorf("endpoint was called %d times for %d events; SKIP LOCKED did not hold", n, events)
	}

	var delivered int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE status = $1`, StatusDelivered).Scan(&delivered); err != nil {
		t.Fatalf("count delivered: %v", err)
	}
	if delivered != events {
		t.Errorf("delivered = %d, want %d", delivered, events)
	}
}

func TestASlowEndpointTimesOutAndIsRetried(t *testing.T) {
	rec := newReceiver(t, 0)
	rec.delay = 3 * time.Second
	h, ctx := newHarness(t, rec.server.URL)
	h.enqueue(t, ctx, `{"id":"evt_slow"}`)

	attempts := h.drain(t, ctx, 1)
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].Status != StatusRetrying {
		t.Errorf("status = %q, want retrying after a timeout", attempts[0].Status)
	}
	if attempts[0].Err == nil {
		t.Error("a timed-out delivery recorded no error")
	}
}

func TestAnEventWithNoSubscribedEndpointQueuesNothing(t *testing.T) {
	rec := newReceiver(t, 0)
	h, ctx := newHarness(t, rec.server.URL)

	if _, err := h.dispatcher.Enqueue(ctx, Event{
		MerchantID: h.merchantID,
		Type:       "payout.settled",
		ResourceID: id.New("po"),
		Payload:    []byte(`{"id":"evt_6"}`),
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var deliveries int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries`).Scan(&deliveries); err != nil {
		t.Fatalf("count: %v", err)
	}
	if deliveries != 0 {
		t.Errorf("queued %d deliveries for an unsubscribed event type, want 0", deliveries)
	}
}
