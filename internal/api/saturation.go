package api

import (
	"net/http"
	"runtime"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/httpx"
)

type Saturation struct {
	MaxConns            int32 `json:"max_conns"`
	TotalConns          int32 `json:"total_conns"`
	AcquiredConns       int32 `json:"acquired_conns"`
	IdleConns           int32 `json:"idle_conns"`
	AcquireCount        int64 `json:"acquire_count"`
	EmptyAcquireCount   int64 `json:"empty_acquire_count"`
	CanceledAcquires    int64 `json:"canceled_acquire_count"`
	AcquireWaitNanos    int64 `json:"acquire_wait_nanos"`
	NewConnsCount       int64 `json:"new_conns_count"`
	Goroutines          int   `json:"goroutines"`
	MaxProcs            int   `json:"max_procs"`
	ConstructingConns   int32 `json:"constructing_conns"`
	MaxLifetimeDestroys int64 `json:"max_lifetime_destroy_count"`
}

func poolStats(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := pool.Stat()
		httpx.WriteJSON(w, http.StatusOK, Saturation{
			MaxConns:            s.MaxConns(),
			TotalConns:          s.TotalConns(),
			AcquiredConns:       s.AcquiredConns(),
			IdleConns:           s.IdleConns(),
			AcquireCount:        s.AcquireCount(),
			EmptyAcquireCount:   s.EmptyAcquireCount(),
			CanceledAcquires:    s.CanceledAcquireCount(),
			AcquireWaitNanos:    s.AcquireDuration().Nanoseconds(),
			NewConnsCount:       s.NewConnsCount(),
			Goroutines:          runtime.NumGoroutine(),
			MaxProcs:            runtime.GOMAXPROCS(0),
			ConstructingConns:   s.ConstructingConns(),
			MaxLifetimeDestroys: s.MaxLifetimeDestroyCount(),
		})
	}
}
