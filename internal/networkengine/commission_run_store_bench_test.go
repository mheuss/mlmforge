package networkengine

import (
	"context"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NFR1's two targets. Reported in MB (1e6), not MiB, matching how the design
// states them — MiB would understate every figure by 5%, biasing toward a
// pass.
const (
	nfr1Rows          = 1_000_000
	nfr1MaxSeconds    = 60.0
	nfr1MaxLiveHeapMB = 256.0
)

const heapObjectsMetric = "/memory/classes/heap/objects:bytes"

// BenchmarkSaveResultsOneMillion measures NFR1's wall-clock target and the
// write path's allocation volume.
//
//	go test ./internal/networkengine/ -run '^$' -bench 'BenchmarkSaveResultsOneMillion$' -benchtime 1x
//
// It deliberately does NOT report a live-heap peak. Sampling live heap under
// default GC pacing measures the pacing, not the write: the collector lets the
// heap grow to live*(1+GOGC/100) before running, so the observed peak is the
// caller's fixture times a ratio, and it barely moves when the write's
// allocation volume changes 15x. That is the same caller-data confound the
// design cited when it rejected process RSS.
// BenchmarkSaveResultsOneMillionLiveHeap measures the real figure.
//
// allocMB/op is the memory number to watch here. It is deterministic, it
// cannot miss a transient, and unlike a sampled peak it responds directly to a
// change in what the write path allocates — which is what a chunking
// regression produces.
func BenchmarkSaveResultsOneMillion(b *testing.B) {
	in := buildBenchRows(b)
	ctx := context.Background()

	var totalAllocMB, worstSeconds float64

	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		store, pool, runID := freshBenchRun(b, ctx)

		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		started := time.Now()

		b.StartTimer()
		if err := store.SaveResults(ctx, runID, "primary", in); err != nil {
			b.Fatalf("SaveResults: %v", err)
		}
		b.StopTimer()

		if s := time.Since(started).Seconds(); s > worstSeconds {
			worstSeconds = s
		}
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		// Max, not last: with -benchtime above 1x the reported figure should
		// be the worst iteration.
		if mb := float64(after.TotalAlloc-before.TotalAlloc) / 1e6; mb > totalAllocMB {
			totalAllocMB = mb
		}
		assertRowsLanded(b, ctx, pool, runID)
	}

	b.ReportMetric(float64(nfr1Rows), "rows/op")
	b.ReportMetric(totalAllocMB, "allocMB/op")
	b.ReportMetric(worstSeconds, "seconds/op")

	// The plan says both NFR1 targets gate the ticket, so this one is
	// enforced rather than merely printed. A benchmark that only reports
	// cannot fail, and a number nobody reads is not a gate.
	if worstSeconds > nfr1MaxSeconds {
		b.Errorf("NFR1: %d rows took %.2fs, target is under %.0fs",
			nfr1Rows, worstSeconds, nfr1MaxSeconds)
	}
}

// BenchmarkSaveResultsOneMillionLiveHeap measures NFR1's memory target: the
// live heap the write path actually holds.
//
//	go test ./internal/networkengine/ -run '^$' -bench BenchmarkSaveResultsOneMillionLiveHeap -benchtime 1x
//
// GC pacing is set to 1% for the duration, so the collector runs almost
// continuously and the sampled peak reflects what the write genuinely holds
// rather than how much slack the collector was willing to allow. Under default
// pacing the peak is a function of the caller's fixture size and GOGC instead.
//
// ns/op from this benchmark is meaningless — near-continuous GC inflates it
// several-fold. The two NFR1 targets are coupled through the collector and
// cannot both be measured accurately in one pass, which is why this is a
// separate benchmark. Read wall clock from BenchmarkSaveResultsOneMillion.
func BenchmarkSaveResultsOneMillionLiveHeap(b *testing.B) {
	in := buildBenchRows(b)
	ctx := context.Background()

	var peakLiveHeapMB float64

	for range b.N {
		store, pool, runID := freshBenchRun(b, ctx)
		mb := measurePeakLiveHeapMB(b, func() {
			if err := store.SaveResults(ctx, runID, "primary", in); err != nil {
				b.Fatalf("SaveResults: %v", err)
			}
		})
		if mb > peakLiveHeapMB {
			peakLiveHeapMB = mb
		}
		assertRowsLanded(b, ctx, pool, runID)
	}

	b.ReportMetric(float64(nfr1Rows), "rows/op")
	b.ReportMetric(peakLiveHeapMB, "peakLiveHeapMB")

	// Zero first. A sampler that never fired reports 0, which would sail
	// through the upper bound below and read as a spectacular pass. The write
	// allocates GBs, so some live-heap growth is certain to be observable.
	if peakLiveHeapMB <= 0 {
		b.Errorf("peak live heap measured as %.2f MB, which means the sampler never observed growth; "+
			"the memory target was not actually measured", peakLiveHeapMB)
	} else if peakLiveHeapMB > nfr1MaxLiveHeapMB {
		b.Errorf("NFR1: peak live heap %.2f MB during the write, target is under %.0f MB",
			peakLiveHeapMB, nfr1MaxLiveHeapMB)
	}
}

// measurePeakLiveHeapMB runs work with GC pacing suppressed and returns the
// largest live-heap growth observed over a post-fixture baseline.
//
// The baseline is taken after a forced GC and after the caller's fixture
// exists, so the delta is what the work adds rather than what the caller
// already holds.
func measurePeakLiveHeapMB(b *testing.B, work func()) float64 {
	b.Helper()
	// 1%, so the heap goal sits just above live and the sampler sees the
	// working set rather than the collector's slack. Restored on the way out.
	defer debug.SetGCPercent(debug.SetGCPercent(1))

	runtime.GC()
	baseline := readHeapBytes(b)

	stop := make(chan struct{})
	peakCh := make(chan uint64, 1)
	var once sync.Once
	closeStop := func() { once.Do(func() { close(stop) }) }
	// A Fatalf inside work Goexits, so without this the ticker would keep
	// firing through container teardown.
	defer closeStop()

	go func() {
		var maxDelta uint64
		sample := []metrics.Sample{{Name: heapObjectsMetric}}
		t := time.NewTicker(2 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				peakCh <- maxDelta
				return
			case <-t.C:
				metrics.Read(sample)
				if sample[0].Value.Kind() != metrics.KindUint64 {
					continue
				}
				if h := sample[0].Value.Uint64(); h > baseline && h-baseline > maxDelta {
					maxDelta = h - baseline
				}
			}
		}
	}()

	work()
	closeStop()
	return float64(<-peakCh) / 1e6
}

// buildBenchRows materializes the caller-side slice. It is built once per
// benchmark and never timed: NFR1 is about what the store costs, not what
// constructing a million inputs costs.
func buildBenchRows(b *testing.B) []CommissionResultInput {
	b.Helper()
	if pgContainer == nil {
		b.Skip("postgres container unavailable")
	}
	if testing.Short() {
		b.Skip("skipping the 1M-row benchmark in short mode")
	}
	in := make([]CommissionResultInput, nfr1Rows)
	for i := range in {
		in[i] = CommissionResultInput{
			EarnerID:     uuid.New(),
			DollarAmount: float64(i) / 100.0,
			Detail:       []byte(`{"v":1,"kind":"commission_earning","level":1,"rate":0.05,"cv_amount":100.0}`),
		}
	}
	return in
}

// freshBenchRun gives each iteration a clean database and an open run.
func freshBenchRun(b *testing.B, ctx context.Context) (*PostgresCommissionRunStore, *pgxpool.Pool, uuid.UUID) {
	b.Helper()
	pool := pgContainer.NewPool(b)
	store := NewPostgresCommissionRunStore(pool)
	runID, err := store.CreateRun(ctx, "2026-01", validHash)
	if err != nil {
		b.Fatalf("CreateRun: %v", err)
	}
	return store, pool, runID
}

// assertRowsLanded makes the timing number trustworthy. Without it a
// regression that skipped work while still returning nil would report a fast
// time, a low peak, and pass.
func assertRowsLanded(b *testing.B, ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID) {
	b.Helper()
	var count int64
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM commission_results WHERE run_id = $1", runID).Scan(&count); err != nil {
		b.Fatalf("count rows: %v", err)
	}
	if count != nfr1Rows {
		b.Fatalf("wrote %d rows, want %d; the timing figure is meaningless", count, nfr1Rows)
	}
}

// readHeapBytes returns live heap object bytes. runtime/metrics is cheap
// enough to poll; runtime.ReadMemStats is not, because it stops the world.
//
// It fails the benchmark on an unknown metric name rather than returning zero.
// runtime/metrics reports an unrecognized name as KindBad instead of erroring,
// and a silent zero here would read as a spectacular pass against a 256 MB
// budget.
func readHeapBytes(b *testing.B) uint64 {
	b.Helper()
	sample := []metrics.Sample{{Name: heapObjectsMetric}}
	metrics.Read(sample)
	if sample[0].Value.Kind() != metrics.KindUint64 {
		b.Fatalf("runtime metric %q is unavailable on this Go build; the heap figure would silently read zero",
			heapObjectsMetric)
	}
	return sample[0].Value.Uint64()
}
