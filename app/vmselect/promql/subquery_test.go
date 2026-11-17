package promql

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/app/vmstorage"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/prompb"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/storage"
)

// TestRollupRateOverSubqueryTrailingData verifies that rollup functions over subqueries
// do not take into account subquery samples beyond the query end time.
//
// The subquery time range is extended by one step beyond ec.End in order to guarantee
// that the aligned subquery grid covers ec.End. The extended subquery samples may be
// built from stale raw samples located before ec.End (e.g. when the series ends at
// ec.End, the extended subquery samples just repeat the last raw sample). Such samples
// must not be used by rollup functions, since they produce incorrect results for
// functions, which use adjacent sample pairs, such as rollup_rate, rollup_deriv,
// rollup_increase or rollup_delta. This especially affects instant queries against
// live data, where the ingested series end at the query time.
//
// See https://github.com/VictoriaMetrics/VictoriaMetrics/issues/8850
func TestRollupRateOverSubqueryTrailingData(t *testing.T) {
	for _, fv := range []struct {
		flag  string
		value string
	}{
		{flag: "storageDataPath", value: t.Name()},
		{flag: "retentionPeriod", value: "100y"},
	} {
		if err := flag.Lookup(fv.flag).Value.Set(fv.value); err != nil {
			t.Fatalf("unable to set %q: %v", fv.flag, err)
		}
	}
	vmstorage.Init(4, 5*time.Second, ResetRollupResultCacheIfNeeded)
	t.Cleanup(func() {
		os.RemoveAll(t.Name())
	})

	// The scenario from https://github.com/VictoriaMetrics/VictoriaMetrics/issues/8850:
	// a 4-core CPU with 10s scrape interval. Core 0 is loaded 100% during the minute 5
	// (00:05:00 - 00:06:00), core 1 is loaded 100% during the minute 18
	// (00:18:00 - 00:19:00). The rest of the time all the cores are idle.
	// The idle counter of a loaded core stops growing, while idle counters
	// of other cores grow by 10 per scrape.
	base := int64(1700000000) // some start (seconds)
	scrape := int64(10)
	var rows []storage.MetricRow
	push := func(cpu string, ts int64, v float64) {
		labels := []prompb.Label{
			{Name: "__name__", Value: "node_cpu_seconds_total"},
			{Name: "cpu", Value: cpu},
			{Name: "mode", Value: "idle"},
			{Name: "instance", Value: "n1"},
		}
		rows = append(rows, storage.MetricRow{
			MetricNameRaw: storage.MarshalMetricNameRaw(nil, labels),
			Timestamp:     ts * 1000,
			Value:         v,
		})
	}
	for cpu := 0; cpu < 4; cpu++ {
		idle := 0.0
		for off := int64(0); off <= 30*60; off += scrape {
			idleIncrease := 10.0
			if (cpu == 0 && off >= 300 && off < 360) || (cpu == 1 && off >= 1080 && off < 1140) {
				idleIncrease = 0
			}
			idle += idleIncrease
			push(fmt.Sprintf("%d", cpu), base+off, idle)
		}
	}
	if err := vmstorage.VMInsertAPI.WriteRows(rows); err != nil {
		t.Fatal(err)
	}
	vmstorage.DebugFlush()

	// Instant query at the end of the 30m interval covered by the data,
	// i.e. at the time when the ingested series end.
	ec := &EvalConfig{
		Context:            context.Background(),
		Start:              (base + 1800) * 1000,
		End:                (base + 1800) * 1000,
		Step:               60 * 1000,
		MaxPointsPerSeries: 1e6,
		MaxSeries:          1000,
		RoundDigits:        100,
	}
	f := func(q string, valueExpected float64) {
		t.Helper()
		result, err := Exec(nil, ec, q, false)
		if err != nil {
			t.Fatalf("unexpected error when executing %q: %s", q, err)
		}
		if len(result) != 1 {
			t.Fatalf("unexpected number of results for %q; got %d; want 1", q, len(result))
		}
		values := result[0].Values
		if len(values) != 1 {
			t.Fatalf("unexpected number of values for %q; got %d; want 1", q, len(values))
		}
		if values[0] != valueExpected {
			t.Fatalf("unexpected value for %q; got %v; want %v", q, values[0], valueExpected)
		}
	}

	// The idle counter of the summed series grows by 4 per second when all the cores
	// are idle. A fully loaded core pauses the growth of its idle counter for one
	// minute, so the minimum rate is reached on the 60s interval between adjacent
	// subquery samples, which covers 50 seconds of the loaded minute:
	// the interval gains 240 - 50 = 190, i.e. the rate is 190/60 = 3 + 1/6.
	minRate := 3.0 + 1.0/6.0
	f(`rollup_rate(sum(node_cpu_seconds_total{mode="idle",instance="n1"})[30m:1m], "min")`, minRate)
	f(`rollup_deriv(sum(node_cpu_seconds_total{mode="idle",instance="n1"})[30m:1m], "min")`, minRate)

	// A fully idle core must have the min rate of 1, since its idle counter
	// grows by 1 per second.
	f(`rollup_rate(node_cpu_seconds_total{mode="idle",instance="n1",cpu="2"}[30m:1m], "min")`, 1)

	// The query from issue #8850: the number of cores minus the min rate of the summed
	// idle counter. Only a single core was loaded at any moment in time, so the result
	// must be close to 1 loaded core instead of the incorrectly summed per-core peaks.
	f(`count(node_cpu_seconds_total{mode="idle",instance="n1"}) - rollup_rate(sum(node_cpu_seconds_total{mode="idle",instance="n1"})[30m:1m], "min")`, 4.0-minRate)
}

func TestTrimValuesAfterTimestamp(t *testing.T) {
	f := func(values []float64, timestamps []int64, maxTimestamp int64, valuesExpected []float64, timestampsExpected []int64) {
		t.Helper()
		valuesGot, timestampsGot := trimValuesAfterTimestamp(values, timestamps, maxTimestamp)
		if len(valuesGot) != len(valuesExpected) {
			t.Fatalf("unexpected number of values for trimValuesAfterTimestamp(%v, %v, %d); got %d; want %d",
				values, timestamps, maxTimestamp, len(valuesGot), len(valuesExpected))
		}
		for i, v := range valuesGot {
			if v != valuesExpected[i] {
				t.Fatalf("unexpected value at position %d for trimValuesAfterTimestamp(%v, %v, %d); got %v; want %v",
					i, values, timestamps, maxTimestamp, v, valuesExpected[i])
			}
		}
		if len(timestampsGot) != len(timestampsExpected) {
			t.Fatalf("unexpected number of timestamps for trimValuesAfterTimestamp(%v, %v, %d); got %d; want %d",
				values, timestamps, maxTimestamp, len(timestampsGot), len(timestampsExpected))
		}
		for i, ts := range timestampsGot {
			if ts != timestampsExpected[i] {
				t.Fatalf("unexpected timestamp at position %d for trimValuesAfterTimestamp(%v, %v, %d); got %d; want %d",
					i, values, timestamps, maxTimestamp, ts, timestampsExpected[i])
			}
		}
	}

	// Empty input.
	f(nil, nil, 100, nil, nil)

	// Nothing to trim.
	f([]float64{1, 2}, []int64{10, 20}, 100, []float64{1, 2}, []int64{10, 20})
	f([]float64{1, 2}, []int64{10, 20}, 20, []float64{1, 2}, []int64{10, 20})

	// Trim the tail.
	f([]float64{1, 2, 3, 4}, []int64{10, 20, 30, 40}, 20, []float64{1, 2}, []int64{10, 20})
	f([]float64{1, 2}, []int64{10, 20}, 15, []float64{1}, []int64{10})

	// Trim everything.
	f([]float64{1, 2}, []int64{10, 20}, 5, nil, nil)
}
