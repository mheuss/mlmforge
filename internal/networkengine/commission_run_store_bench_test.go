package networkengine

import (
	"context"
	"runtime"
	"runtime/metrics"
	"testing"
	"time"

	"github.com/google/uuid"
)

// BenchmarkSaveResultsOneMillion measures both NFR1 targets: 1M rows in under
// 60s, and under 256 MB peak Go heap attributable to the write. Run it
// explicitly; it is skipped in short mode so the normal suite stays fast.
//
//	go test ./internal/networkengine/ -run '^$' -bench BenchmarkSaveResultsOneMillion -benchtime 1x
func BenchmarkSaveResultsOneMillion(b *testing.B) {
	if pgContainer == nil {
		b.Skip("postgres container unavailable")
	}
	if testing.Short() {
		b.Skip("skipping the 1M-row benchmark in short mode")
	}

	const rows = 1_000_000
	in := make([]CommissionResultInput, rows)
	for i := range in {
		in[i] = CommissionResultInput{
			EarnerID:     uuid.New(),
			DollarAmount: float64(i) / 100.0,
			Detail:       []byte(`{"v":1,"level":1,"rate":0.05,"cv_amount":100.0}`),
		}
	}

	ctx := context.Background()
	var peakHeapDeltaMB, totalAllocMB float64

	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		pool := pgContainer.NewPool(b)
		store := NewPostgresCommissionRunStore(pool)
		runID, err := store.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			b.Fatalf("CreateRun: %v", err)
		}

		// Baseline after the input slice is already built, so the delta below
		// reflects what the write path costs rather than what the fixture
		// costs. NFR1's target is about the former.
		runtime.GC()
		baseline := readHeapBytes()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)

		// Sample the live heap while SaveResults runs, tracking the maximum
		// delta over the baseline. Reading once after the call would report
		// the heap at that instant, not the peak during it, and would miss
		// transient chunk allocations entirely.
		//
		// runtime/metrics, not runtime.ReadMemStats: ReadMemStats stops the
		// world on every call, and sampling that often would distort the
		// wall-clock number sitting right next to this one.
		//
		// Sampling has a floor — a spike shorter than the interval can be
		// missed. allocMB/op below is the deterministic companion. It cannot
		// miss anything, but it measures cumulative allocation rather than
		// live heap, so the two answer different questions.
		stop := make(chan struct{})
		peakCh := make(chan uint64, 1)
		go func() {
			var maxDelta uint64
			t := time.NewTicker(5 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stop:
					peakCh <- maxDelta
					return
				case <-t.C:
					if h := readHeapBytes(); h > baseline && h-baseline > maxDelta {
						maxDelta = h - baseline
					}
				}
			}
		}()

		b.StartTimer()
		if err := store.SaveResults(ctx, runID, "primary", in); err != nil {
			b.Fatalf("SaveResults: %v", err)
		}
		b.StopTimer()

		close(stop)
		if mb := float64(<-peakCh) / (1 << 20); mb > peakHeapDeltaMB {
			peakHeapDeltaMB = mb
		}
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		// Max, not last: with -benchtime above 1x the reported figure should
		// be the worst iteration, matching how peakHeapDeltaMB is kept.
		if mb := float64(after.TotalAlloc-before.TotalAlloc) / (1 << 20); mb > totalAllocMB {
			totalAllocMB = mb
		}
		// The timer stays stopped at the loop edge. Restarting it here would
		// time the loop bookkeeping and the next iteration's setup up to its
		// StopTimer, which is not the write path.
	}

	b.ReportMetric(float64(rows), "rows/op")
	b.ReportMetric(peakHeapDeltaMB, "peakHeapDeltaMB")
	b.ReportMetric(totalAllocMB, "allocMB/op")
}

// readHeapBytes returns live heap object bytes. runtime/metrics is cheap
// enough to poll; runtime.ReadMemStats is not, because it stops the world.
func readHeapBytes() uint64 {
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(sample)
	if sample[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return sample[0].Value.Uint64()
}
