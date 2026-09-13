package wallet

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/money"
)

func implementations(t *testing.T) map[string]Reserver {
	t.Helper()
	impls := map[string]Reserver{"memory": NewMemory()}

	addr := os.Getenv("MARSPAY_TEST_REDIS_ADDR")
	if addr == "" {
		t.Log("MARSPAY_TEST_REDIS_ADDR unset, skipping the Redis implementation")
		return impls
	}

	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis ping: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	impls["redis"] = NewRedis(client, "test:"+id.ULID()+":", time.Minute)
	return impls
}

func TestReserveDecrementsWhenFundsAreAvailable(t *testing.T) {
	ctx := context.Background()
	for name, w := range implementations(t) {
		t.Run(name, func(t *testing.T) {
			acc := id.New("acc")
			if err := w.Warm(ctx, acc, money.FromRupiah(100_000)); err != nil {
				t.Fatalf("warm: %v", err)
			}

			remaining, err := w.Reserve(ctx, acc, money.FromRupiah(32_000))
			if err != nil {
				t.Fatalf("reserve: %v", err)
			}
			if want := money.FromRupiah(68_000); remaining != want {
				t.Errorf("remaining = %d, want %d", remaining, want)
			}
		})
	}
}

func TestReserveRefusesToGoNegative(t *testing.T) {
	ctx := context.Background()
	for name, w := range implementations(t) {
		t.Run(name, func(t *testing.T) {
			acc := id.New("acc")
			if err := w.Warm(ctx, acc, money.FromRupiah(10_000)); err != nil {
				t.Fatalf("warm: %v", err)
			}

			if _, err := w.Reserve(ctx, acc, money.FromRupiah(10_001)); !errors.Is(err, ErrInsufficientFunds) {
				t.Fatalf("got %v, want ErrInsufficientFunds", err)
			}

			available, err := w.Available(ctx, acc)
			if err != nil {
				t.Fatalf("available: %v", err)
			}
			if want := money.FromRupiah(10_000); available != want {
				t.Errorf("balance changed on a rejected reserve: %d, want %d", available, want)
			}
		})
	}
}

func TestReserveOnUnknownAccountIsAnError(t *testing.T) {
	ctx := context.Background()
	for name, w := range implementations(t) {
		t.Run(name, func(t *testing.T) {
			if _, err := w.Reserve(ctx, id.New("acc"), 100); !errors.Is(err, ErrAccountNotCached) {
				t.Errorf("got %v, want ErrAccountNotCached", err)
			}
		})
	}
}

func TestConcurrentReserveNeverOverspends(t *testing.T) {
	ctx := context.Background()
	for name, w := range implementations(t) {
		t.Run(name, func(t *testing.T) {
			acc := id.New("acc")
			const funded = 100
			const attempts = 400

			if err := w.Warm(ctx, acc, money.Minor(funded)); err != nil {
				t.Fatalf("warm: %v", err)
			}

			var wg sync.WaitGroup
			var mu sync.Mutex
			granted := 0

			for i := 0; i < attempts; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := w.Reserve(ctx, acc, 1)
					if err == nil {
						mu.Lock()
						granted++
						mu.Unlock()
						return
					}
					if !errors.Is(err, ErrInsufficientFunds) {
						t.Errorf("unexpected error: %v", err)
					}
				}()
			}
			wg.Wait()

			if granted != funded {
				t.Errorf("granted %d reservations, want exactly %d", granted, funded)
			}

			remaining, err := w.Available(ctx, acc)
			if err != nil {
				t.Fatalf("available: %v", err)
			}
			if remaining != 0 {
				t.Errorf("remaining = %d, want 0", remaining)
			}
		})
	}
}

func TestReleasePutsFundsBack(t *testing.T) {
	ctx := context.Background()
	for name, w := range implementations(t) {
		t.Run(name, func(t *testing.T) {
			acc := id.New("acc")
			if err := w.Warm(ctx, acc, money.FromRupiah(50_000)); err != nil {
				t.Fatalf("warm: %v", err)
			}
			if _, err := w.Reserve(ctx, acc, money.FromRupiah(20_000)); err != nil {
				t.Fatalf("reserve: %v", err)
			}

			after, err := w.Release(ctx, acc, money.FromRupiah(20_000))
			if err != nil {
				t.Fatalf("release: %v", err)
			}
			if want := money.FromRupiah(50_000); after != want {
				t.Errorf("after release = %d, want %d", after, want)
			}
		})
	}
}
