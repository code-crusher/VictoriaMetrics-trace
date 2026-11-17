package writer

import (
	"bytes"
	"fmt"
	"math/rand"
	"sync"
	"testing"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/decimal"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/encoding"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/protoparser/native/stream"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/protoparser/protoparserutil"
)

func TestWriteBlock(t *testing.T) {
	protoparserutil.StartUnmarshalWorkers()
	defer protoparserutil.StopUnmarshalWorkers()

	f := func(metricName string, timestamps []int64, values []float64, precisionBits uint8) {
		t.Helper()

		data, err := WriteBlock(nil, metricName, timestamps, values, precisionBits)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		blocksRead := 0
		err = stream.Parse(bytes.NewReader(data), "", func(block *stream.Block) error {
			blocksRead++
			if string(block.MetricName.MetricGroup) != "foo" {
				t.Fatalf("unexpected metric group: %q; want %q", block.MetricName.MetricGroup, "foo")
			}
			if len(block.Timestamps) != len(timestamps) {
				t.Fatalf("unexpected number of timestamps: %d; want %d", len(block.Timestamps), len(timestamps))
			}
			for i, ts := range block.Timestamps {
				if ts != timestamps[i] {
					t.Fatalf("unexpected timestamp at position %d: %d; want %d", i, ts, timestamps[i])
				}
			}
			if len(block.Values) != len(values) {
				t.Fatalf("unexpected number of values: %d; want %d", len(block.Values), len(values))
			}
			for i, v := range block.Values {
				if precisionBits == 64 {
					if v != values[i] {
						t.Fatalf("unexpected value at position %d: %v; want %v", i, v, values[i])
					}
					continue
				}
				// Lossy encoding is allowed for precisionBits < 64.
				if fmt.Sprintf("%.4f", v) != fmt.Sprintf("%.4f", values[i]) {
					t.Fatalf("unexpected value at position %d: %v; want approximately %v", i, v, values[i])
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}
		if blocksRead != 1 {
			t.Fatalf("unexpected number of blocks read: %d; want 1", blocksRead)
		}
	}

	// Single sample
	f(`foo{bar="baz"}`, []int64{1000}, []float64{123.456}, 64)
	// Multiple samples
	f(`foo{bar="baz"}`, []int64{1000, 2000, 3000, 4000}, []float64{1, 2, 3, 4}, 64)
	// No tags
	f(`foo`, []int64{1000, 2000}, []float64{1, 2}, 64)
	// Multiple tags
	f(`foo{aaa="1",bbb="2",ccc="3"}`, []int64{1000, 2000, 3000}, []float64{-1.5, 0, 1.5}, 64)
	// Values with decimals
	f(`foo{bar="baz"}`, []int64{1000, 2000, 3000}, []float64{0.1, 0.01, 0.001}, 64)
	// Lossy encoding
	f(`foo{bar="baz"}`, []int64{1000, 2000, 3000, 4000}, []float64{1, 2, 3, 4}, 8)
	// Large number of samples
	timestamps := make([]int64, 1000)
	values := make([]float64, len(timestamps))
	for i := range timestamps {
		timestamps[i] = int64(1000 + i)
		values[i] = float64(i) + 0.5
	}
	f(`foo{bar="baz"}`, timestamps, values, 64)
}

func TestWriteBlockErrors(t *testing.T) {
	// Mismatched timestamps and values
	if _, err := WriteBlock(nil, `foo{bar="baz"}`, []int64{1000, 2000}, []float64{1}, 64); err == nil {
		t.Fatalf("expecting non-nil error for mismatched timestamps and values")
	}
	// Invalid precisionBits
	if _, err := WriteBlock(nil, `foo{bar="baz"}`, []int64{1000}, []float64{1}, 0); err == nil {
		t.Fatalf("expecting non-nil error for zero precisionBits")
	}
	if _, err := WriteBlock(nil, `foo{bar="baz"}`, []int64{1000}, []float64{1}, 65); err == nil {
		t.Fatalf("expecting non-nil error for too big precisionBits")
	}
	// Unsorted timestamps
	if _, err := WriteBlock(nil, `foo{bar="baz"}`, []int64{2000, 1000}, []float64{1, 2}, 64); err == nil {
		t.Fatalf("expecting non-nil error for unsorted timestamps")
	}
	// Invalid metric name
	if _, err := WriteBlock(nil, "invalid metric name", []int64{1000}, []float64{1}, 64); err == nil {
		t.Fatalf("expecting non-nil error for invalid metricName")
	}
	// Empty timestamps - no error, no data
	data, err := WriteBlock(nil, `foo{bar="baz"}`, nil, nil, 64)
	if err != nil {
		t.Fatalf("unexpected error for empty timestamps: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("unexpected non-empty data for empty timestamps: %d bytes", len(data))
	}
}

func TestStreamWriteBlock(t *testing.T) {
	protoparserutil.StartUnmarshalWorkers()
	defer protoparserutil.StopUnmarshalWorkers()

	type block struct {
		metricName string
		timestamps []int64
		values     []float64
	}
	var blocks []block
	for i := 0; i < 3; i++ {
		metricName := fmt.Sprintf(`foo{bar="baz%d"}`, i)
		timestamps := make([]int64, 10)
		values := make([]float64, len(timestamps))
		for j := range timestamps {
			timestamps[j] = int64(1000 + i*10 + j)
			values[j] = float64(i*10+j) + 0.25
		}
		blocks = append(blocks, block{metricName, timestamps, values})
	}

	var bb bytes.Buffer
	s, err := NewStream(&bb, 1000, 1029)
	if err != nil {
		t.Fatalf("unexpected error on NewStream: %v", err)
	}
	for i := range blocks {
		if err := s.WriteBlock(blocks[i].metricName, blocks[i].timestamps, blocks[i].values, 64); err != nil {
			t.Fatalf("unexpected error when writing block %d: %v", i, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("unexpected error when closing stream: %v", err)
	}

	// The parser can call the callback concurrently from multiple worker
	// goroutines, so identify each parsed block by its metric name instead
	// of relying on the order.
	var parsedLock sync.Mutex
	parsed := make([]int, len(blocks))
	err = stream.Parse(bytes.NewReader(bb.Bytes()), "", func(b *stream.Block) error {
		blockIdx := -1
		for i := range blocks {
			if blocks[i].metricName == b.MetricName.String() {
				blockIdx = i
				break
			}
		}
		if blockIdx < 0 {
			return fmt.Errorf("unexpected metric name %q", b.MetricName.String())
		}
		want := &blocks[blockIdx]
		parsedLock.Lock()
		parsed[blockIdx]++
		parsedLock.Unlock()
		if len(b.Timestamps) != len(want.timestamps) {
			return fmt.Errorf("unexpected number of timestamps: %d; want %d", len(b.Timestamps), len(want.timestamps))
		}
		for i, ts := range b.Timestamps {
			if ts != want.timestamps[i] {
				return fmt.Errorf("unexpected timestamp at position %d: %d; want %d", i, ts, want.timestamps[i])
			}
		}
		for i, v := range b.Values {
			if v != want.values[i] {
				return fmt.Errorf("unexpected value at position %d: %v; want %v", i, v, want.values[i])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	for i, n := range parsed {
		if n != 1 {
			t.Fatalf("unexpected number of parsed blocks for block %d: %d; want 1", i, n)
		}
	}

	// Verify the stream header contains the passed time range.
	gotMin, gotMax := getTimeRange(bb.Bytes())
	if gotMin != 1000 || gotMax != 1029 {
		t.Fatalf("unexpected time range in the stream header: [%d, %d]; want [1000, 1029]", gotMin, gotMax)
	}
}

func TestStreamTimeRangeFiltering(t *testing.T) {
	protoparserutil.StartUnmarshalWorkers()
	defer protoparserutil.StopUnmarshalWorkers()

	// Samples outside the stream time range must be filtered out
	// by the server during importing.
	timestamps := []int64{1000, 2000, 3000, 4000, 5000}
	values := []float64{1, 2, 3, 4, 5}

	var bb bytes.Buffer
	// The time range covers only the samples in the middle.
	s, err := NewStream(&bb, 2000, 4000)
	if err != nil {
		t.Fatalf("unexpected error on NewStream: %v", err)
	}
	if err := s.WriteBlock(`foo{bar="baz"}`, timestamps, values, 64); err != nil {
		t.Fatalf("unexpected error on WriteBlock: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("unexpected error on Close: %v", err)
	}

	rowsRead := 0
	err = stream.Parse(bytes.NewReader(bb.Bytes()), "", func(b *stream.Block) error {
		rowsRead += len(b.Timestamps)
		wantTimestamps := []int64{2000, 3000, 4000}
		wantValues := []float64{2, 3, 4}
		if len(b.Timestamps) != len(wantTimestamps) {
			return fmt.Errorf("unexpected number of timestamps: %d; want %d", len(b.Timestamps), len(wantTimestamps))
		}
		for i, ts := range b.Timestamps {
			if ts != wantTimestamps[i] {
				return fmt.Errorf("unexpected timestamp at position %d: %d; want %d", i, ts, wantTimestamps[i])
			}
		}
		for i, v := range b.Values {
			if v != wantValues[i] {
				return fmt.Errorf("unexpected value at position %d: %v; want %v", i, v, wantValues[i])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if rowsRead != 3 {
		t.Fatalf("unexpected number of rows read: %d; want 3", rowsRead)
	}
}

func TestStreamNewStreamErrors(t *testing.T) {
	if _, err := NewStream(&bytes.Buffer{}, 2000, 1000); err == nil {
		t.Fatalf("expecting non-nil error when minTimestamp > maxTimestamp")
	}
}

func TestStreamWriteBlockAfterError(t *testing.T) {
	s, err := NewStream(&bytes.Buffer{}, 1000, 2000)
	if err != nil {
		t.Fatalf("unexpected error on NewStream: %v", err)
	}
	if err := s.WriteBlock(`foo`, []int64{2000, 1000}, []float64{1, 2}, 64); err == nil {
		t.Fatalf("expecting non-nil error for unsorted timestamps")
	}
	// Subsequent writes must fail with the same error.
	if err := s.WriteBlock(`foo`, []int64{1000}, []float64{1}, 64); err == nil {
		t.Fatalf("expecting non-nil error after the previous error")
	}
	if err := s.Close(); err == nil {
		t.Fatalf("expecting non-nil error on Close after the previous error")
	}
}

func TestStreamPool(t *testing.T) {
	protoparserutil.StartUnmarshalWorkers()
	defer protoparserutil.StopUnmarshalWorkers()

	timestamps := []int64{1000, 2000, 3000}
	values := []float64{1, 2, 3}

	var bb bytes.Buffer
	s, err := NewStream(&bb, 1000, 3000)
	if err != nil {
		t.Fatalf("unexpected error on NewStream: %v", err)
	}
	if err := s.WriteBlock(`foo{bar="baz"}`, timestamps, values, 64); err != nil {
		t.Fatalf("unexpected error on WriteBlock: %v", err)
	}
	if err := PutStream(s); err != nil {
		t.Fatalf("unexpected error on PutStream: %v", err)
	}

	blocksRead := 0
	err = stream.Parse(bytes.NewReader(bb.Bytes()), "", func(b *stream.Block) error {
		blocksRead++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if blocksRead != 1 {
		t.Fatalf("unexpected number of blocks read: %d; want 1", blocksRead)
	}
}

func TestWriteBlockRandomData(t *testing.T) {
	protoparserutil.StartUnmarshalWorkers()
	defer protoparserutil.StopUnmarshalWorkers()

	rnd := rand.New(rand.NewSource(0))
	f := func(precisionBits uint8) {
		t.Helper()

		type block struct {
			metricName string
			timestamps []int64
			values     []float64
		}
		var blocks []block

		var bb bytes.Buffer
		minTimestamp := int64(1 << 40)
		maxTimestamp := minTimestamp
		blocksTotal := rnd.Intn(20) + 1
		s, err := NewStream(&bb, 0, MaxTimestamp)
		if err != nil {
			t.Fatalf("unexpected error on NewStream: %v", err)
		}
		for i := 0; i < blocksTotal; i++ {
			metricName := fmt.Sprintf(`foo{bar="baz%d"}`, i)
			rows := rnd.Intn(100) + 1
			timestamps := make([]int64, rows)
			values := make([]float64, rows)
			ts := rnd.Int63n(1 << 40)
			for j := 0; j < rows; j++ {
				timestamps[j] = ts
				ts += rnd.Int63n(2 * 1000)
				values[j] = rnd.Float64() * 1_000_000
			}
			if timestamps[0] < minTimestamp {
				minTimestamp = timestamps[0]
			}
			if timestamps[rows-1] > maxTimestamp {
				maxTimestamp = timestamps[rows-1]
			}
			blocks = append(blocks, block{metricName, timestamps, values})
			if err := s.WriteBlock(metricName, timestamps, values, precisionBits); err != nil {
				t.Fatalf("unexpected error on WriteBlock: %v", err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatalf("unexpected error on Close: %v", err)
		}

		// The parser can call the callback concurrently from multiple
		// worker goroutines, so identify each parsed block by its metric
		// name instead of relying on the order.
		var parsedLock sync.Mutex
		parsed := make([]int, len(blocks))
		err = stream.Parse(bytes.NewReader(bb.Bytes()), "", func(b *stream.Block) error {
			// Find the matching block by metric name, since the callback
			// can be called in arbitrary order.
			blockIdx := -1
			for i := range blocks {
				if blocks[i].metricName == b.MetricName.String() {
					blockIdx = i
					break
				}
			}
			if blockIdx < 0 {
				return fmt.Errorf("unexpected metric name %q", b.MetricName.String())
			}
			want := &blocks[blockIdx]
			parsedLock.Lock()
			parsed[blockIdx]++
			parsedLock.Unlock()
			if len(b.Timestamps) != len(want.timestamps) {
				return fmt.Errorf("unexpected number of timestamps: %d; want %d", len(b.Timestamps), len(want.timestamps))
			}
			for i, ts := range b.Timestamps {
				// Timestamps are encoded with lossy compression
				// for precisionBits < 64, so only verify them exactly
				// for the lossless case.
				if precisionBits == 64 && ts != want.timestamps[i] {
					return fmt.Errorf("unexpected timestamp at position %d: %d; want %d", i, ts, want.timestamps[i])
				}
			}
			if len(b.Values) != len(want.values) {
				return fmt.Errorf("unexpected number of values: %d; want %d", len(b.Values), len(want.values))
			}
			for i, v := range b.Values {
				// The decimal encoding used by the native format may lose
				// precision for values bigger than 1e6 (they are rounded
				// to 15 significant digits), and precisionBits < 64 allows
				// additional lossy compression. Mirror this behavior here.
				// See lib/decimal for details.
				wantValue := decimal.RoundToSignificantFigures(want.values[i], 15)
				if precisionBits == 64 && v != wantValue {
					return fmt.Errorf("unexpected value at position %d: %v; want %v", i, v, want.values[i])
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}
		for i, n := range parsed {
			if n != 1 {
				t.Fatalf("unexpected number of parsed blocks for block %d: %d; want 1", i, n)
			}
		}
	}

	f(64)
	f(24)
	f(8)
	f(1)
}

func getTimeRange(data []byte) (int64, int64) {
	if len(data) < 16 {
		return 0, 0
	}
	return encoding.UnmarshalInt64(data), encoding.UnmarshalInt64(data[8:])
}
