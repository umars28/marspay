package admission

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func blocking(release <-chan struct{}, entered chan<- struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	})
}

func call(h http.Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/payments", nil))
	return rec
}

func TestQuietTrafficIsNeverShed(t *testing.T) {
	l := New(Config{InFlight: 4, Queue: 8, MaxWait: time.Second})
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 200; i++ {
		if rec := call(h); rec.Code != http.StatusOK {
			t.Fatalf("request %d got %d, want 200: a limiter that sheds an idle system is useless", i, rec.Code)
		}
	}

	s := l.Stats()
	if s.ShedQueueFull+s.ShedWaitTooLong != 0 {
		t.Errorf("shed %d requests while never exceeding capacity", s.ShedQueueFull+s.ShedWaitTooLong)
	}
	if s.Queueing != 0 {
		t.Errorf("%d requests queued when a slot was always free", s.Queueing)
	}
}

func TestCapacityIsTheNumberOfConcurrentHandlers(t *testing.T) {
	const capacity = 3
	release := make(chan struct{})
	entered := make(chan struct{}, 16)

	l := New(Config{InFlight: capacity, Queue: 16, MaxWait: 2 * time.Second})
	h := l.Middleware(blocking(release, entered))

	var wg sync.WaitGroup
	for i := 0; i < capacity; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); call(h) }()
	}

	for i := 0; i < capacity; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of %d handlers started", i, capacity)
		}
	}

	select {
	case <-entered:
		t.Fatal("a fourth handler ran; the limit is not enforced")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	wg.Wait()
}

func TestAFullQueueIsRefusedImmediately(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 8)

	l := New(Config{InFlight: 1, Queue: 1, MaxWait: 10 * time.Second})
	h := l.Middleware(blocking(release, entered))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); call(h) }()
	<-entered

	wg.Add(1)
	go func() { defer wg.Done(); call(h) }()
	waitForQueue(t, l, 1)

	began := time.Now()
	rec := call(h)
	took := time.Since(began)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", rec.Code)
	}
	if took > time.Second {
		t.Errorf("refusal took %s; a full queue must be refused without waiting", took)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("a 503 without Retry-After tells the caller nothing about when to come back")
	}

	close(release)
	wg.Wait()

	if s := l.Stats(); s.ShedQueueFull != 1 {
		t.Errorf("shed_queue_full = %d, want 1", s.ShedQueueFull)
	}
}

func TestWaitingTooLongIsShedRatherThanQueued(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 8)

	l := New(Config{InFlight: 1, Queue: 64, MaxWait: 40 * time.Millisecond})
	h := l.Middleware(blocking(release, entered))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); call(h) }()
	<-entered

	began := time.Now()
	rec := call(h)
	took := time.Since(began)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", rec.Code)
	}
	if took > 500*time.Millisecond {
		t.Errorf("waited %s before shedding, want roughly 40ms", took)
	}

	close(release)
	wg.Wait()

	if s := l.Stats(); s.ShedWaitTooLong != 1 {
		t.Errorf("shed_wait_too_long = %d, want 1", s.ShedWaitTooLong)
	}
}

func TestAQueuedRequestIsAdmittedWhenASlotFrees(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 8)

	l := New(Config{InFlight: 1, Queue: 8, MaxWait: 5 * time.Second})
	h := l.Middleware(blocking(release, entered))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); call(h) }()
	<-entered

	var second atomic.Int32
	wg.Add(1)
	go func() { defer wg.Done(); second.Store(int32(call(h).Code)) }()
	waitForQueue(t, l, 1)

	close(release)
	<-entered
	wg.Wait()

	if second.Load() != http.StatusOK {
		t.Errorf("the queued request got %d, want 200", second.Load())
	}
	if s := l.Stats(); s.Queueing != 1 {
		t.Errorf("admitted_after_queueing = %d, want 1", s.Queueing)
	}
}

func TestAPanickingHandlerDoesNotLeakItsSlot(t *testing.T) {
	l := New(Config{InFlight: 1, Queue: 1, MaxWait: time.Second})
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("handler exploded")
	}))

	func() {
		defer func() { _ = recover() }()
		call(h)
	}()

	done := make(chan int, 1)
	go func() {
		ok := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		done <- call(ok).Code
	}()

	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Errorf("got %d after a panic, want 200", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the slot was never released; one panic would take the service down permanently")
	}
}

func TestAClientThatGivesUpFreesItsQueueSlot(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 8)

	l := New(Config{InFlight: 1, Queue: 4, MaxWait: 10 * time.Second})
	h := l.Middleware(blocking(release, entered))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); call(h) }()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodPost, "/v1/payments", nil).WithContext(ctx)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}()
	waitForQueue(t, l, 1)

	cancel()
	waitForQueue(t, l, 0)

	close(release)
	wg.Wait()

	s := l.Stats()
	if s.Abandoned != 1 {
		t.Errorf("abandoned_while_queued = %d, want 1", s.Abandoned)
	}
	if s.ShedQueueFull+s.ShedWaitTooLong != 0 {
		t.Errorf("a caller giving up was counted as shedding: %+v", s)
	}
}

func TestUnderOverloadTheAdmittedStayFast(t *testing.T) {
	const capacity = 4
	l := New(Config{InFlight: capacity, Queue: capacity, MaxWait: 30 * time.Millisecond})
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))

	var (
		mu       sync.Mutex
		slowest  time.Duration
		admitted int
		shedded  int
		wg       sync.WaitGroup
	)

	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			began := time.Now()
			code := call(h).Code
			took := time.Since(began)

			mu.Lock()
			defer mu.Unlock()
			switch code {
			case http.StatusOK:
				admitted++
				if took > slowest {
					slowest = took
				}
			case http.StatusServiceUnavailable:
				shedded++
			}
		}()
	}
	wg.Wait()

	if shedded == 0 {
		t.Fatal("200 concurrent callers against 4 slots shed nothing; the limiter did not engage")
	}
	if admitted == 0 {
		t.Fatal("nothing was admitted; the limiter shed everything")
	}
	if slowest > 500*time.Millisecond {
		t.Errorf("an admitted request took %s; shedding is supposed to protect latency, not just count", slowest)
	}
}

func waitForQueue(t *testing.T, l *Limiter, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.Stats().Queued == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue depth never reached %d, stuck at %d", want, l.Stats().Queued)
}
