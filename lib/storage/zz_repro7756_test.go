//go:build synctest

package storage

import (
	"fmt"
	"math/rand"
	"testing"
	"testing/synctest"
	"time"
)

// TestRepro7756_midnight measures where the per-day index registration work
// goes during the last hour of a day and right after midnight.
func TestRepro7756_midnight(t *testing.T) {
	defer testRemoveAll(t)

	synctest.Test(t, func(t *testing.T) {
		sleepUntil(t, 2000, 1, 1, 22, 0, 0, 0)

		const numSeries = 20000
		s := MustOpenStorage(t.Name(), OpenOptions{})
		defer s.MustClose()

		rng := rand.New(rand.NewSource(1))
		mrs := testGenerateMetricRowsWithPrefix(rng, numSeries, "metric", TimeRange{
			MinTimestamp: time.Now().UnixMilli(),
			MaxTimestamp: time.Now().UnixMilli() + 1000,
		})

		ingest := func() {
			now := time.Now().UnixMilli()
			for j := range mrs {
				mrs[j].Timestamp = now
			}
			s.AddRows(mrs, defaultPrecisionBits)
			s.DebugFlush()
			synctest.Wait()
			time.Sleep(15 * time.Second)
			synctest.Wait()
		}

		prev := struct {
			fast, slow, miss, hasSlow, preCreated, slowPDI, newTS uint64
		}{}
		report := func(label string) {
			var m Metrics
			s.UpdateMetrics(&m)
			fast := expDmcFastHits.Load()
			slow := expDmcSlowHits.Load()
			miss := expDmcMisses.Load()
			hasSlow := expDmcHasSlowCall.Load()
			preCreated := m.TimeseriesPreCreated
			slowPDI := m.SlowPerDayIndexInserts
			newTS := m.NewTimeseriesCreated
			fmt.Printf("%s t=%s dmcFast=%d dmcSlow=%d dmcMiss=%d hasSlowCalls=%d preCreated=%d slowPerDayInserts=%d newTS=%d\n",
				label, time.Now().UTC().Format("15:04:05"),
				fast-prev.fast, slow-prev.slow, miss-prev.miss, hasSlow-prev.hasSlow,
				preCreated-prev.preCreated, slowPDI-prev.slowPDI, newTS-prev.newTS)
			prev.fast, prev.slow, prev.miss, prev.hasSlow = fast, slow, miss, hasSlow
			prev.preCreated, prev.slowPDI, prev.newTS = preCreated, slowPDI, newTS
		}

		// Warm up for 30 minutes so caches are populated and rotations settle.
		for range 30 * 4 {
			ingest()
		}
		report("warmup-end")

		// Last hour before midnight: 4 samples per minute.
		for i := range 60 * 4 {
			ingest()
			if i%15 == 14 {
				report(fmt.Sprintf("prefill-%02dm", (i+1)/4))
			}
		}
		// After midnight.
		for i := range 20 * 4 {
			ingest()
			if i%15 == 14 {
				report(fmt.Sprintf("after-%02dm", (i+1)/4))
			}
		}
	})
}
