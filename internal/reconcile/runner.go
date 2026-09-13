package reconcile

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/money"
)

func minor(v *int64) money.Minor {
	return money.Minor(*v)
}

type Run struct {
	ID           string
	BusinessDate time.Time
	ProviderCode string
	Report       Report
	Duration     time.Duration
}

type Runner struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewRunner(pool *pgxpool.Pool) *Runner {
	return &Runner{pool: pool, now: time.Now}
}

func (r *Runner) Run(ctx context.Context, providerCode string, date time.Time, internal, provider []Line) (*Run, error) {
	started := r.now()
	report := Compare(internal, provider)

	run := &Run{
		ID:           id.New("rec"),
		BusinessDate: date,
		ProviderCode: providerCode,
		Report:       report,
		Duration:     r.now().Sub(started),
	}

	if err := r.persist(ctx, run); err != nil {
		return nil, err
	}
	return run, nil
}

func (r *Runner) persist(ctx context.Context, run *Run) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	status := "clean"
	if !run.Report.Clean() {
		status = "differences"
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO reconciliation_runs
		   (id, business_date, provider_code, rows_compared, rows_matched,
		    discrepancies, delta_minor, status, finished_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		 ON CONFLICT (business_date, provider_code) DO UPDATE SET
		   rows_compared = EXCLUDED.rows_compared,
		   rows_matched  = EXCLUDED.rows_matched,
		   discrepancies = EXCLUDED.discrepancies,
		   delta_minor   = EXCLUDED.delta_minor,
		   status        = EXCLUDED.status,
		   finished_at   = EXCLUDED.finished_at`,
		run.ID, run.BusinessDate, run.ProviderCode,
		run.Report.RowsCompared, run.Report.RowsMatched,
		len(run.Report.Discrepancies), int64(run.Report.Delta), status)
	if err != nil {
		return fmt.Errorf("reconcile: insert run: %w", err)
	}

	var runID string
	err = tx.QueryRow(ctx,
		`SELECT id FROM reconciliation_runs WHERE business_date = $1 AND provider_code = $2`,
		run.BusinessDate, run.ProviderCode).Scan(&runID)
	if err != nil {
		return fmt.Errorf("reconcile: read run id: %w", err)
	}
	run.ID = runID

	for _, d := range run.Report.Discrepancies {
		var internal, provider *int64
		if d.Internal != nil {
			v := int64(*d.Internal)
			internal = &v
		}
		if d.Provider != nil {
			v := int64(*d.Provider)
			provider = &v
		}

		_, err = tx.Exec(ctx,
			`INSERT INTO reconciliation_discrepancies
			   (id, run_id, external_ref, internal_minor, provider_minor,
			    delta_minor, suspected_cause, resolution)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending')`,
			id.New("rdf"), runID, d.Ref, internal, provider,
			int64(d.Delta), string(d.Cause))
		if err != nil {
			return fmt.Errorf("reconcile: insert discrepancy %s: %w", d.Ref, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("reconcile: commit: %w", err)
	}
	return nil
}

func (r *Runner) Open(ctx context.Context) ([]Discrepancy, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT external_ref, internal_minor, provider_minor, delta_minor, suspected_cause
		 FROM reconciliation_discrepancies
		 WHERE resolution IS NULL OR resolution = 'pending'
		 ORDER BY created_at, external_ref`)
	if err != nil {
		return nil, fmt.Errorf("reconcile: open discrepancies: %w", err)
	}
	defer rows.Close()

	var out []Discrepancy
	for rows.Next() {
		var d Discrepancy
		var internal, provider *int64
		var delta int64
		var cause string

		if err := rows.Scan(&d.Ref, &internal, &provider, &delta, &cause); err != nil {
			return nil, fmt.Errorf("reconcile: scan discrepancy: %w", err)
		}
		d.Delta = minor(&delta)
		d.Cause = Cause(cause)
		if internal != nil {
			v := minor(internal)
			d.Internal = &v
		}
		if provider != nil {
			v := minor(provider)
			d.Provider = &v
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *Runner) Resolve(ctx context.Context, ref, resolution, actor string) error {
	if resolution == "" || actor == "" {
		return fmt.Errorf("reconcile: resolving a difference needs a resolution and an actor")
	}

	tag, err := r.pool.Exec(ctx,
		`UPDATE reconciliation_discrepancies
		 SET resolution = $1, resolved_by = $2, resolved_at = now()
		 WHERE external_ref = $3 AND (resolution IS NULL OR resolution = 'pending')`,
		resolution, actor, ref)
	if err != nil {
		return fmt.Errorf("reconcile: resolve %s: %w", ref, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("reconcile: no open difference for %s", ref)
	}
	return nil
}
