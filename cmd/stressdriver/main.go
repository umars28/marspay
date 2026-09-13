package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type saturation struct {
	MaxConns          int32 `json:"max_conns"`
	TotalConns        int32 `json:"total_conns"`
	AcquiredConns     int32 `json:"acquired_conns"`
	AcquireCount      int64 `json:"acquire_count"`
	EmptyAcquireCount int64 `json:"empty_acquire_count"`
	AcquireWaitNanos  int64 `json:"acquire_wait_nanos"`
	Goroutines        int   `json:"goroutines"`
	MaxProcs          int   `json:"max_procs"`
}

type step struct {
	Concurrency int
	Completed   int64
	Errors      int64
	NonCreated  int64
	Elapsed     time.Duration
	Latencies   []time.Duration
	PoolWait    time.Duration
	PoolEmpty   int64
	PoolAcquire int64
	Goroutines  int
	PeakConns   int32
	Sample      string
}

func (s step) rate() float64 {
	if s.Elapsed == 0 {
		return 0
	}
	return float64(s.Completed) / s.Elapsed.Seconds()
}

func (s step) percentile(p float64) time.Duration {
	if len(s.Latencies) == 0 {
		return 0
	}
	idx := int(float64(len(s.Latencies)-1) * p)
	return s.Latencies[idx]
}

func (s step) mean() time.Duration {
	if len(s.Latencies) == 0 {
		return 0
	}
	var total time.Duration
	for _, d := range s.Latencies {
		total += d
	}
	return total / time.Duration(len(s.Latencies))
}

func (s step) waitPerRequest() time.Duration {
	if s.Completed == 0 {
		return 0
	}
	return s.PoolWait / time.Duration(s.Completed)
}

func main() {
	base := flag.String("base", "http://127.0.0.1:8099", "API base URL")
	user := flag.String("user", "usr_load", "consumer id prefix used as the bearer token")
	users := flag.Int("users", 500, "distinct consumers to rotate through")
	merchant := flag.String("merchant", "merch_load", "merchant id to pay")
	runID := flag.String("run", "stress", "idempotency key prefix, must be unique per run")
	levels := flag.String("levels", "1,2,4,8,16,32,64,128,256,384", "concurrency steps")
	dwell := flag.Duration("dwell", 6*time.Second, "measured time at each step")
	warmup := flag.Duration("warmup", 1500*time.Millisecond, "unmeasured time at each step")
	flag.Parse()

	steps, err := parseLevels(*levels)
	if err != nil {
		fatal(err)
	}

	if *users < 1 {
		fatal(errors.New("users must be positive"))
	}

	start, err := fetchSaturation(*base)
	if err != nil {
		fatal(err)
	}

	fmt.Printf("==> %s per step after %s of warm-up, stepping %s across %d consumers\n",
		dwell.Round(time.Millisecond), warmup.Round(time.Millisecond), *levels, *users)
	fmt.Printf("==> the API holds a pool of %d connections on %d cores\n\n",
		start.MaxConns, start.MaxProcs)

	var results []step
	for _, level := range steps {
		r, err := runStep(*base, *user, *users, *merchant, *runID, level, *dwell, *warmup)
		if err != nil {
			fatal(err)
		}
		results = append(results, r)
		printRow(r)

		if r.Errors > r.Completed/10 && r.Completed > 0 {
			fmt.Printf("\n==> stopping: more than 10%% of requests failed at %d\n", level)
			break
		}
	}

	fmt.Println()
	analyse(results)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "stressdriver:", err)
	os.Exit(1)
}

func parseLevels(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("bad concurrency level %q: %w", part, err)
		}
		if n < 1 {
			return nil, fmt.Errorf("concurrency must be positive, got %d", n)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, errors.New("no concurrency levels given")
	}
	return out, nil
}

func client(concurrency int) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        concurrency * 2,
			MaxIdleConnsPerHost: concurrency * 2,
			MaxConnsPerHost:     concurrency * 2,
			IdleConnTimeout:     60 * time.Second,
			DisableCompression:  true,
		},
	}
}

func runStep(base, userPrefix string, users int, merchantID, runID string, concurrency int, dwell, warmup time.Duration) (step, error) {
	hc := client(concurrency)
	body, err := json.Marshal(map[string]any{
		"merchant_id": merchantID,
		"method":      "qris",
		"amount":      3_200_000,
		"currency":    "IDR",
	})
	if err != nil {
		return step{}, err
	}

	var seq atomic.Int64
	post := func(ctx context.Context) (time.Duration, int, string, error) {
		n := seq.Add(1)
		key := fmt.Sprintf("%s-%d-%d", runID, concurrency, n)
		user := fmt.Sprintf("%s_%d", userPrefix, n%int64(users))
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			base+"/v1/payments", bytes.NewReader(body))
		if err != nil {
			return 0, 0, "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+user)
		req.Header.Set("Idempotency-Key", key)

		began := time.Now()
		resp, err := hc.Do(req)
		if err != nil {
			return time.Since(began), 0, "", err
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode == http.StatusCreated {
			_, _ = io.Copy(io.Discard, resp.Body)
			return time.Since(began), resp.StatusCode, "", nil
		}
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return time.Since(began), resp.StatusCode, string(detail), nil
	}

	drive := func(d time.Duration, record bool) (step, error) {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()

		var (
			mu        sync.Mutex
			wg        sync.WaitGroup
			latencies []time.Duration
			completed int64
			failures  int64
			nonCreate int64
			sample    string
		)

		start := time.Now()
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ctx.Err() == nil {
					took, status, detail, err := post(ctx)
					if ctx.Err() != nil {
						return
					}
					mu.Lock()
					switch {
					case err != nil:
						failures++
						if sample == "" {
							sample = err.Error()
						}
					case status == http.StatusCreated:
						completed++
						if record {
							latencies = append(latencies, took)
						}
					default:
						nonCreate++
						failures++
						if sample == "" {
							sample = fmt.Sprintf("HTTP %d %s", status, detail)
						}
					}
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		return step{
			Concurrency: concurrency,
			Completed:   completed,
			Errors:      failures,
			NonCreated:  nonCreate,
			Elapsed:     time.Since(start),
			Latencies:   latencies,
			Sample:      sample,
		}, nil
	}

	if _, err := drive(warmup, false); err != nil {
		return step{}, err
	}

	before, err := fetchSaturation(base)
	if err != nil {
		return step{}, err
	}

	measured, err := drive(dwell, true)
	if err != nil {
		return step{}, err
	}

	after, err := fetchSaturation(base)
	if err != nil {
		return step{}, err
	}

	measured.PoolWait = time.Duration(after.AcquireWaitNanos - before.AcquireWaitNanos)
	measured.PoolEmpty = after.EmptyAcquireCount - before.EmptyAcquireCount
	measured.PoolAcquire = after.AcquireCount - before.AcquireCount
	measured.Goroutines = after.Goroutines
	measured.PeakConns = after.MaxConns
	return measured, nil
}

func fetchSaturation(base string) (saturation, error) {
	resp, err := http.Get(base + "/internal/v1/saturation")
	if err != nil {
		return saturation{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return saturation{}, fmt.Errorf("saturation endpoint returned %d", resp.StatusCode)
	}

	var s saturation
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return saturation{}, err
	}
	return s, nil
}

var headerPrinted bool

func printRow(s step) {
	if !headerPrinted {
		fmt.Printf("%7s %9s %9s %9s %9s %8s %11s %9s %7s\n",
			"clients", "ops/s", "p50", "p95", "p99", "errors", "poolwait/req", "waited%", "gor")
		fmt.Println(strings.Repeat("-", 88))
		headerPrinted = true
	}

	waited := 0.0
	if s.PoolAcquire > 0 {
		waited = float64(s.PoolEmpty) / float64(s.PoolAcquire) * 100
	}

	fmt.Printf("%7d %9.0f %9s %9s %9s %8d %11s %8.0f%% %7d\n",
		s.Concurrency, s.rate(),
		round(s.percentile(0.50)), round(s.percentile(0.95)), round(s.percentile(0.99)),
		s.Errors, round(s.waitPerRequest()), waited, s.Goroutines)

	if s.Sample != "" {
		fmt.Printf("        first failure: %s\n", strings.TrimSpace(s.Sample))
	}
}

func analyse(results []step) {
	if len(results) == 0 {
		return
	}

	best := results[0]
	for _, r := range results {
		if r.rate() > best.rate() {
			best = r
		}
	}

	var plateau []step
	for _, r := range results {
		if r.rate() >= best.rate()*0.9 {
			plateau = append(plateau, r)
		}
	}

	fmt.Printf("Where it stops scaling (pool of %d connections):\n", best.PeakConns)
	fmt.Printf("  peak throughput   : %.0f ops/s at %d clients\n", best.rate(), best.Concurrency)
	if len(plateau) > 1 {
		fmt.Printf("  plateau           : %d to %d clients all land within 10%% of peak\n",
			plateau[0].Concurrency, plateau[len(plateau)-1].Concurrency)
	}

	var saturated *step
	for i := range results {
		if results[i].PoolAcquire == 0 {
			continue
		}
		if float64(results[i].PoolEmpty)/float64(results[i].PoolAcquire) > 0.5 {
			saturated = &results[i]
			break
		}
	}

	if saturated == nil {
		fmt.Println("\nWhat saturated:")
		fmt.Println("  the connection pool never ran empty; the limit is elsewhere")
		return
	}

	fmt.Printf("  queue forms at    : %d clients, where %.0f%% of database acquisitions "+
		"find an empty pool\n",
		saturated.Concurrency,
		float64(saturated.PoolEmpty)/float64(saturated.PoolAcquire)*100)

	last := results[len(results)-1]
	if last.Concurrency > saturated.Concurrency {
		fmt.Printf("  past that         : %dx the clients buys %.2fx the throughput "+
			"and costs %.1fx the p99\n",
			last.Concurrency/saturated.Concurrency,
			last.rate()/saturated.rate(),
			float64(last.percentile(0.99))/float64(saturated.percentile(0.99)))
	}

	fmt.Println("\nWhat saturated:")
	fmt.Printf("  at %d clients the mean request takes %s, of which %s is spent waiting "+
		"for a pooled connection (%.0f%%)\n",
		last.Concurrency, round(last.mean()), round(last.waitPerRequest()),
		float64(last.waitPerRequest())/float64(last.mean())*100)
	fmt.Println("  the pool is the queue: the database is never asked to do more than it can")
}

func round(d time.Duration) string {
	switch {
	case d >= time.Second:
		return d.Round(10 * time.Millisecond).String()
	case d >= time.Millisecond:
		return d.Round(100 * time.Microsecond).String()
	default:
		return d.Round(time.Microsecond).String()
	}
}
