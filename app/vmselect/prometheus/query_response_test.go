package prometheus

import (
	"strings"
	"testing"

	"github.com/VictoriaMetrics/VictoriaMetrics/app/vmselect/netstorage"
	"github.com/VictoriaMetrics/VictoriaMetrics/app/vmselect/promql"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/storage"
)

func testResult(metricGroup string, ts int64, v float64) netstorage.Result {
	return netstorage.Result{
		MetricName: storage.MetricName{
			MetricGroup: []byte(metricGroup),
		},
		Timestamps: []int64{ts},
		Values:     []float64{v},
	}
}

func TestQueryResponseLimit(t *testing.T) {
	rs := []netstorage.Result{
		testResult("up", 1000, 1),
		testResult("up", 1000, 1),
		testResult("up", 1000, 1),
	}
	var qs promql.QueryStats
	noopDone := func() {}
	f := func(limit int, expectedResultCount int) {
		t.Helper()
		s := QueryResponse(rs, nil, noopDone, &qs, limit)
		if n := strings.Count(s, `"metric"`); n != expectedResultCount {
			t.Fatalf("unexpected number of results; got %d; want %d", n, expectedResultCount)
		}
	}
	f(0, 3)
	f(1, 1)
	f(2, 2)
	f(3, 3)
	f(10, 3)
}

func TestQueryRangeResponseLimit(t *testing.T) {
	rs := []netstorage.Result{
		testResult("up", 1000, 1),
		testResult("up", 2000, 1),
		testResult("up", 3000, 1),
	}
	var qs promql.QueryStats
	noopDone := func() {}
	f := func(limit int, expectedResultCount int) {
		t.Helper()
		s := QueryRangeResponse(rs, nil, noopDone, &qs, limit)
		if n := strings.Count(s, `"metric"`); n != expectedResultCount {
			t.Fatalf("unexpected number of results; got %d; want %d", n, expectedResultCount)
		}
	}
	f(0, 3)
	f(1, 1)
	f(2, 2)
	f(3, 3)
	f(10, 3)
}

func TestQueryResponseLimitValidJSON(t *testing.T) {
	// Make sure that the truncated output remains valid JSON.
	rs := []netstorage.Result{
		testResult("up", 1000, 1),
	}
	var qs promql.QueryStats
	noopDone := func() {}
	s := QueryResponse(rs, nil, noopDone, &qs, 1)
	if !strings.HasSuffix(strings.TrimSpace(s), "}") {
		t.Fatalf("response is not valid JSON: %q", s)
	}
}
