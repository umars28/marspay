package admission

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/umars28/marspay/internal/httpx"
)

type Config struct {
	InFlight int
	Queue    int
	MaxWait  time.Duration
}

func (c Config) withDefaults() Config {
	if c.InFlight < 1 {
		c.InFlight = 1
	}
	if c.Queue < 0 {
		c.Queue = 0
	}
	if c.MaxWait <= 0 {
		c.MaxWait = 50 * time.Millisecond
	}
	return c
}

type Limiter struct {
	cfg   Config
	slots chan struct{}

	queued   atomic.Int64
	admitted atomic.Int64
	queueHit atomic.Int64
	shedFull atomic.Int64
	shedWait atomic.Int64
	abandon  atomic.Int64
	waitedNs atomic.Int64
	peakQ    atomic.Int64
}

func New(cfg Config) *Limiter {
	cfg = cfg.withDefaults()
	return &Limiter{cfg: cfg, slots: make(chan struct{}, cfg.InFlight)}
}

type Stats struct {
	InFlightLimit   int   `json:"in_flight_limit"`
	QueueLimit      int   `json:"queue_limit"`
	MaxWaitMillis   int64 `json:"max_wait_ms"`
	InFlight        int   `json:"in_flight"`
	Queued          int64 `json:"queued"`
	PeakQueued      int64 `json:"peak_queued"`
	Admitted        int64 `json:"admitted"`
	Queueing        int64 `json:"admitted_after_queueing"`
	ShedQueueFull   int64 `json:"shed_queue_full"`
	ShedWaitTooLong int64 `json:"shed_wait_too_long"`
	Abandoned       int64 `json:"abandoned_while_queued"`
	QueueWaitNanos  int64 `json:"queue_wait_nanos"`
}

func (l *Limiter) Stats() Stats {
	return Stats{
		InFlightLimit:   l.cfg.InFlight,
		QueueLimit:      l.cfg.Queue,
		MaxWaitMillis:   l.cfg.MaxWait.Milliseconds(),
		InFlight:        len(l.slots),
		Queued:          l.queued.Load(),
		PeakQueued:      l.peakQ.Load(),
		Admitted:        l.admitted.Load(),
		Queueing:        l.queueHit.Load(),
		ShedQueueFull:   l.shedFull.Load(),
		ShedWaitTooLong: l.shedWait.Load(),
		Abandoned:       l.abandon.Load(),
		QueueWaitNanos:  l.waitedNs.Load(),
	}
}

func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case l.slots <- struct{}{}:
			l.admitted.Add(1)
			defer func() { <-l.slots }()
			next.ServeHTTP(w, r)
			return
		default:
		}

		depth := l.queued.Add(1)
		l.recordPeak(depth)
		if depth > int64(l.cfg.Queue) {
			l.queued.Add(-1)
			l.shedFull.Add(1)
			shed(w, r, "The service is at capacity.")
			return
		}

		timer := time.NewTimer(l.cfg.MaxWait)
		defer timer.Stop()
		began := time.Now()

		select {
		case l.slots <- struct{}{}:
			l.queued.Add(-1)
			l.waitedNs.Add(int64(time.Since(began)))
			l.queueHit.Add(1)
			l.admitted.Add(1)
			defer func() { <-l.slots }()

			if r.Context().Err() != nil {
				l.abandon.Add(1)
				return
			}
			next.ServeHTTP(w, r)

		case <-timer.C:
			l.queued.Add(-1)
			l.waitedNs.Add(int64(time.Since(began)))
			l.shedWait.Add(1)
			shed(w, r, "The service is busy; the request was not started.")

		case <-r.Context().Done():
			l.queued.Add(-1)
			l.waitedNs.Add(int64(time.Since(began)))
			l.abandon.Add(1)
		}
	})
}

func (l *Limiter) recordPeak(depth int64) {
	for {
		peak := l.peakQ.Load()
		if depth <= peak || l.peakQ.CompareAndSwap(peak, depth) {
			return
		}
	}
}

func shed(w http.ResponseWriter, r *http.Request, message string) {
	w.Header().Set("Retry-After", strconv.Itoa(1))
	httpx.WriteError(w, r, httpx.Errorf(http.StatusServiceUnavailable,
		httpx.TypeOverloaded, "%s", message))
}

func Skip(match func(*http.Request) bool, limited func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		guarded := limited(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if match(r) {
				next.ServeHTTP(w, r)
				return
			}
			guarded.ServeHTTP(w, r)
		})
	}
}
