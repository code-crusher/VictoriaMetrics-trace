package storage

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func newDateMetricIDCacheShard() *dateMetricIDCacheShard {
	var c dateMetricIDCacheShard
	c.prev = newByDateMetricIDMap()
	c.next = newByDateMetricIDMap()
	c.curr.Store(newByDateMetricIDMap())
	return &c
}

func TestDateMetricIDCacheShardSerial(t *testing.T) {
	c := newDateMetricIDCacheShard()
	if err := testDateMetricIDCacheShard(c, false); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
}

func TestDateMetricIDCacheShardConcurrent(t *testing.T) {
	c := newDateMetricIDCacheShard()
	ch := make(chan error, 5)
	for range 5 {
		go func() {
			ch <- testDateMetricIDCacheShard(c, true)
		}()
	}
	for range 5 {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
		case <-time.After(time.Second * 5):
			t.Fatalf("timeout")
		}
	}
}

func testDateMetricIDCacheShard(c *dateMetricIDCacheShard, concurrent bool) error {
	type dmk struct {
		date     uint64
		metricID uint64
	}
	m := make(map[dmk]bool)
	for i := range int(1e5) {
		date := uint64(i) % 2
		metricID := uint64(i) % 1237
		if !concurrent && c.Has(date, metricID) {
			if !m[dmk{date, metricID}] {
				return fmt.Errorf("c.Has(%d, %d) must return false, but returned true", date, metricID)
			}
			continue
		}
		c.Set(date, metricID)
		m[dmk{date, metricID}] = true
		if !concurrent && !c.Has(date, metricID) {
			return fmt.Errorf("c.Has(%d, %d) must return true, but returned false", date, metricID)
		}
		if i%11234 == 0 {
			c.mu.Lock()
			c.syncLocked()
			c.mu.Unlock()
		}
		if i%34323 == 0 {
			// Two rotations are needed to clear the cache.
			c.rotate()
			c.rotate()
			m = make(map[dmk]bool)
		}
	}

	// Verify fast path after sync.
	for i := range int(1e5) {
		date := uint64(i) % 2
		metricID := uint64(i) % 123
		c.Set(date, metricID)
	}
	c.mu.Lock()
	c.syncLocked()
	c.mu.Unlock()
	for i := range int(1e5) {
		date := uint64(i) % 2
		metricID := uint64(i) % 123
		if !concurrent && !c.Has(date, metricID) {
			return fmt.Errorf("c.Has(%d, %d) must return true after sync", date, metricID)
		}
	}

	// Verify that cache becomes empty after two rotations.
	if n := c.Stats().Size; !concurrent && n < 123 {
		return fmt.Errorf("c.EntriesCount must return at least 123; returned %d", n)
	}
	c.rotate()
	if n := c.Stats().Size; !concurrent && n < 123 {
		return fmt.Errorf("c.EntriesCount must return at least 123; returned %d", n)
	}
	c.rotate()
	if n := c.Stats().Size; !concurrent && n > 0 {
		return fmt.Errorf("c.EntriesCount must return 0 after reset; returned %d", n)
	}
	return nil
}

// TestDateMetricIDCacheMidnightBurstKeepsEntries verifies that the cache
// keeps serving the entries during the midnight ingestion burst, when all
// the active time series are looked up under the new date at once, while the
// cache also holds the entries for the previous date and the entries
// prefilled for the new date.
//
// This is a regression test for
// https://github.com/VictoriaMetrics/VictoriaMetrics/issues/7756
// ("vmstorage insert slow down during indexDb rotation", observed on
// v1.104.0-cluster as a daily slowdown at 00:00 UTC).
//
// The root cause of the issue was in dateMetricIDCache.syncLocked(), which
// used to reset the whole cache once its published part exceeded
// memory.Allowed()/256 bytes:
//
//	if dmc.SizeBytes() > uint64(memory.Allowed())/256 {
//		dmc.resetLocked()
//	}
//
// At midnight the cache always holds roughly double the steady-state number
// of entries, since it holds the entries for the previous date plus the
// entries prefilled for the new date during the last hour of the previous
// day (see updatePerDateData()). The midnight ingestion burst spikes
// slowHits and triggers syncLocked(), which publishes the doubled-size map
// and immediately reset the whole cache on large setups. After the reset,
// every per-day lookup missed in the cache and fell back to the on-disk
// indexdb search for every ingested row, which resulted in slow inserts
// spike, CPU spike and ingestion slowdown right after 00:00 UTC (08:00
// UTC+8), every day, until the cache warmed up again.
//
// The cache is no longer reset - it is rotated instead, one shard at a time
// (see d9c07dbc0, cd2e11b7c and 4d4c1ff72). This test fails if the
// midnight burst ever resets the cache or loses the already added entries
// again.
func TestDateMetricIDCacheMidnightBurstKeepsEntries(t *testing.T) {
	const (
		date      = 100
		numSeries = 200_000
	)

	c := newDateMetricIDCache()
	defer c.MustStop()

	// Simulate the steady state of day `date`: the entries for all the series
	// are added to the cache and then synced into curr (the published part)
	// by the lookups below.
	for i := uint64(1); i <= numSeries; i++ {
		c.Set(date, i)
	}
	for i := uint64(1); i <= numSeries; i++ {
		if !c.Has(date, i) {
			t.Fatalf("lost entry (date=%d, metricID=%d) after the sync", date, i)
		}
	}
	statsSteady := c.Stats()
	t.Logf("steady state: %+v", statsSteady)

	// Simulate the last hour of day `date`: the per-day index prefill adds
	// the entries for the next date, doubling the cache contents.
	for i := uint64(1); i <= numSeries; i++ {
		c.Set(date+1, i)
	}

	// Midnight burst: all the series are looked up under the new date at
	// once. This spikes slowHits and triggers syncs in all the shards that
	// hold the entries.
	for i := uint64(1); i <= numSeries; i++ {
		if !c.Has(date+1, i) {
			t.Fatalf("lost entry (date=%d, metricID=%d) during the midnight burst", date+1, i)
		}
	}
	statsAfterBurst := c.Stats()
	t.Logf("after midnight burst: %+v", statsAfterBurst)

	// The syncs must actually happen during the burst, otherwise the test
	// doesn't exercise the sync behavior at all.
	if statsAfterBurst.SyncsCount <= statsSteady.SyncsCount {
		t.Fatalf("unexpected number of syncs: got %d, want > %d", statsAfterBurst.SyncsCount, statsSteady.SyncsCount)
	}

	// The cache must keep all the entries for both dates after the burst.
	for i := uint64(1); i <= numSeries; i++ {
		if !c.Has(date, i) {
			t.Fatalf("lost entry (date=%d, metricID=%d) after the midnight burst", date, i)
		}
		if !c.Has(date+1, i) {
			t.Fatalf("lost entry (date=%d, metricID=%d) after the midnight burst", date+1, i)
		}
	}
	if got, want := c.Stats().Size, uint64(2*numSeries); got != want {
		t.Fatalf("unexpected cache size after the midnight burst: got %d, want %d", got, want)
	}
}

func TestDateMetricIDCacheIsConsistent(_ *testing.T) {
	const (
		generation  = 1
		date        = 1
		concurrency = 2
		numMetrics  = 100000
	)
	dmc := newDateMetricIDCache()
	defer dmc.MustStop()
	var wg sync.WaitGroup
	for i := range concurrency {
		wg.Go(func() {
			for id := uint64(i * numMetrics); id < uint64((i+1)*numMetrics); id++ {
				dmc.Set(date, id)
				if !dmc.Has(date, id) {
					panic(fmt.Errorf("dmc.Has(metricID=%d): unexpected cache miss after adding the entry to cache", id))
				}
			}
		})
	}
	wg.Wait()
}

func TestDateMetricIDCache_Size(t *testing.T) {
	dmc := newDateMetricIDCache()
	defer dmc.MustStop()
	for i := range 100_000 {
		date := 12345 + uint64(i%30)
		metricID := uint64(i)
		dmc.Set(date, metricID)

		if got, want := dmc.Stats().Size, uint64(i+1); got != want {
			t.Fatalf("unexpected size: got %d, want %d", got, want)
		}
	}

	// Retrieve all entries and check the cache size again.
	for i := range 100_000 {
		date := 12345 + uint64(i%30)
		metricID := uint64(i)
		if !dmc.Has(date, metricID) {
			t.Fatalf("entry not in cache: (date=%d, metricID=%d)", date, metricID)
		}
	}
	if got, want := dmc.Stats().Size, uint64(100_000); got != want {
		t.Fatalf("unexpected size: got %d, want %d", got, want)
	}
}
