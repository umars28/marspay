package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/money"
	"github.com/umars28/marspay/internal/rail"
	"github.com/umars28/marspay/internal/testdb"
)

var businessDate = time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)

func newRunner(t *testing.T) (*Runner, context.Context) {
	t.Helper()
	pool, ctx := testdb.New(t)
	return NewRunner(pool), ctx
}

func driveSim(t *testing.T, lossRate float64, count int) (internal, provider []Line, lost int) {
	t.Helper()

	sim, err := rail.NewSim(rail.Config{
		Rail:     rail.Name("qris_switch"),
		Outcomes: map[rail.Outcome]float64{rail.OutcomeCallbackLost: lossRate},
		Seed:     99,
	})
	if err != nil {
		t.Fatalf("new sim: %v", err)
	}

	for i := 0; i < count; i++ {
		amount := money.Minor(1_000_000 + i*1_000)
		result, err := sim.Send(context.Background(), rail.Request{
			PayoutID:  id.New("tup"),
			BankCode:  "BCA",
			AccountNo: "4471",
			Amount:    amount,
		})
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}

		if result.CallbackSent {
			internal = append(internal, Line{Ref: result.BankReference, Amount: amount})
		} else {
			lost++
		}
	}

	for _, s := range sim.Statement() {
		provider = append(provider, Line{Ref: s.BankReference, Amount: s.Amount})
	}
	return internal, provider, lost
}

func TestLostCallbacksFromTheSimulatorSurfaceAsDifferences(t *testing.T) {
	runner, ctx := newRunner(t)

	internal, provider, lost := driveSim(t, 0.15, 200)
	if lost == 0 {
		t.Fatal("the simulator lost no callbacks, so this test proves nothing")
	}

	run, err := runner.Run(ctx, "qris_switch", businessDate, internal, provider)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := len(run.Report.Discrepancies); got != lost {
		t.Fatalf("discrepancies = %d, want %d (one per lost callback)", got, lost)
	}
	for _, d := range run.Report.Discrepancies {
		if d.Cause != CauseProviderOnly {
			t.Errorf("cause = %q, want %q", d.Cause, CauseProviderOnly)
		}
	}
	if run.Report.RowsMatched != len(internal) {
		t.Errorf("matched = %d, want %d", run.Report.RowsMatched, len(internal))
	}
	if run.Report.Delta >= 0 {
		t.Errorf("delta = %d, want negative: the provider settled money we never booked",
			run.Report.Delta)
	}

	open, err := runner.Open(ctx)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(open) != lost {
		t.Errorf("open differences = %d, want %d", len(open), lost)
	}

	t.Logf("%d transfers, %d callbacks lost, %d differences found, delta %s",
		200, lost, len(run.Report.Discrepancies), run.Report.Delta)
}

func TestPerfectDayReconcilesClean(t *testing.T) {
	runner, ctx := newRunner(t)

	internal, provider, lost := driveSim(t, 0, 100)
	if lost != 0 {
		t.Fatalf("expected no losses, got %d", lost)
	}

	run, err := runner.Run(ctx, "qris_switch", businessDate, internal, provider)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if !run.Report.Clean() {
		t.Errorf("expected a clean run, got %d differences", len(run.Report.Discrepancies))
	}
	if run.Report.RowsMatched != 100 {
		t.Errorf("matched = %d, want 100", run.Report.RowsMatched)
	}

	open, err := runner.Open(ctx)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("clean run still left %d open differences", len(open))
	}
}

func TestRunIsIdempotentForTheSameBusinessDate(t *testing.T) {
	runner, ctx := newRunner(t)
	internal, provider, _ := driveSim(t, 0.1, 50)

	first, err := runner.Run(ctx, "qris_switch", businessDate, internal, provider)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := runner.Run(ctx, "qris_switch", businessDate, internal, provider)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	if first.ID != second.ID {
		t.Errorf("rerunning the same business date created a second run: %s vs %s",
			first.ID, second.ID)
	}

	var runs int
	if err := runner.pool.QueryRow(ctx,
		`SELECT count(*) FROM reconciliation_runs`).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runs != 1 {
		t.Errorf("reconciliation_runs has %d rows, want 1", runs)
	}
}

func TestResolvingADifferenceRemovesItFromTheQueue(t *testing.T) {
	runner, ctx := newRunner(t)

	internal := []Line{}
	provider := []Line{{Ref: "qr-8791-1c04", Amount: 9_100_000}}

	if _, err := runner.Run(ctx, "qris_switch", businessDate, internal, provider); err != nil {
		t.Fatalf("run: %v", err)
	}

	open, err := runner.Open(ctx)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open = %d, want 1", len(open))
	}

	if err := runner.Resolve(ctx, "qr-8791-1c04", "posted_manually", "umar@marspay"); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	open, err = runner.Open(ctx)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("resolved difference is still in the queue")
	}
}

func TestResolveRequiresAnActorAndAResolution(t *testing.T) {
	runner, ctx := newRunner(t)

	if err := runner.Resolve(ctx, "qr-1", "", "umar@marspay"); err == nil {
		t.Error("resolving without a resolution was accepted")
	}
	if err := runner.Resolve(ctx, "qr-1", "posted_manually", ""); err == nil {
		t.Error("resolving without an actor was accepted")
	}
}

func TestResolvingSomethingThatIsNotOpenFails(t *testing.T) {
	runner, ctx := newRunner(t)

	err := runner.Resolve(ctx, "qr-does-not-exist", "posted_manually", "umar@marspay")
	if err == nil {
		t.Error("resolving a non-existent difference was accepted")
	}
}
