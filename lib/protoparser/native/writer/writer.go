// Package writer allows building /api/v1/import/native data streams
// without a running VictoriaMetrics instance.
//
// It produces the same binary layout as /api/v1/export/native, so any
// VictoriaMetrics single-node server, vminsert or vmagent accepts the
// result at /api/v1/import/native (also known as the VictoriaMetrics native
// ingestion protocol). This is the most efficient protocol for importing
// data, since the data is transferred in a compact binary form with
// effective compression.
//
// Usage:
//
//	s, err := NewStream(w, minTimestamp, maxTimestamp)
//	if err != nil {
//		// handle error
//	}
//	defer s.MustClose()
//	if err := s.WriteBlock(`foo{bar="baz"}`, timestamps, values, 64); err != nil {
//		// handle error
//	}
package writer

import (
	"fmt"
	"sync"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/decimal"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/encoding"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promutil"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/storage"
)

// WriteBlock marshals the given samples for the given metricName in native
// ingestion protocol format, appends the result to dst and returns it.
//
// The result is a full-featured stream, which can be sent to
// /api/v1/import/native as is. The stream contains the header with the time
// range of the given samples, so it must contain samples for a single block
// only. Use Stream for writing multiple blocks into a single stream.
//
// metricName must contain the metric name in `metric{tag1="value1",...}` form.
// See storage.MetricName for details.
//
// timestamps must contain non-decreasing values, i.e. the samples in the
// block must be sorted by timestamp.
//
// precisionBits must be in the range [1...64], where 1 means 50% precision,
// while 64 means 100% precision, i.e. lossless encoding.
func WriteBlock(dst []byte, metricName string, timestamps []int64, values []float64, precisionBits uint8) ([]byte, error) {
	if len(timestamps) == 0 {
		return dst, nil
	}
	if len(timestamps) != len(values) {
		return dst, fmt.Errorf("the number of timestamps doesn't match the number of values; got %d vs %d", len(timestamps), len(values))
	}
	if err := encoding.CheckPrecisionBits(precisionBits); err != nil {
		return dst, err
	}
	for i := 1; i < len(timestamps); i++ {
		if timestamps[i] < timestamps[i-1] {
			return dst, fmt.Errorf("timestamps must be non-decreasing; got %d at position %d, which is smaller than %d at position %d",
				timestamps[i], i, timestamps[i-1], i-1)
		}
	}

	// The server uses the time range from the header for filtering out
	// samples with timestamps outside the range.
	dst = encoding.MarshalInt64(dst, timestamps[0])
	dst = encoding.MarshalInt64(dst, timestamps[len(timestamps)-1])

	return marshalBlock(dst, metricName, timestamps, values, precisionBits)
}

// marshalBlock marshals the given samples for the given metricName in native
// ingestion protocol format, appends the result to dst and returns it.
//
// The stream header isn't written by this function. See WriteBlock and
// Stream.WriteBlock, which handle the header.
func marshalBlock(dst []byte, metricName string, timestamps []int64, values []float64, precisionBits uint8) ([]byte, error) {
	if len(timestamps) == 0 {
		return dst, nil
	}
	if len(timestamps) != len(values) {
		return dst, fmt.Errorf("the number of timestamps doesn't match the number of values; got %d vs %d", len(timestamps), len(values))
	}
	if err := encoding.CheckPrecisionBits(precisionBits); err != nil {
		return dst, err
	}
	for i := 1; i < len(timestamps); i++ {
		if timestamps[i] < timestamps[i-1] {
			return dst, fmt.Errorf("timestamps must be non-decreasing; got %d at position %d, which is smaller than %d at position %d",
				timestamps[i], i, timestamps[i-1], i-1)
		}
	}

	labels, err := promutil.NewLabelsFromString(metricName)
	if err != nil {
		return dst, fmt.Errorf("cannot parse metricName %q: %w", metricName, err)
	}
	defer promutil.PutLabels(labels)

	mn := storage.GetMetricName()
	defer storage.PutMetricName(mn)
	for _, label := range labels.GetLabels() {
		if label.Name == "__name__" {
			mn.MetricGroup = append(mn.MetricGroup[:0], label.Value...)
			continue
		}
		mn.AddTag(label.Name, label.Value)
	}

	// There is no need in bringing mn.Tags to the canonical form
	// (via mn.sortTags()), since the server re-canonicalizes the metric
	// name during ingestion.
	mnBuf := mn.Marshal(nil)
	dst = encoding.MarshalUint32(dst, uint32(len(mnBuf)))
	dst = append(dst, mnBuf...)

	vb := valuesBufPool.Get().(*valuesBuf)
	var scale int16
	vb.b, scale = decimal.AppendFloatToDecimal(vb.b[:0], values)
	b := &storage.Block{}
	var tsid storage.TSID
	b.Init(&tsid, timestamps, vb.b, scale, precisionBits)
	blockBuf := b.MarshalPortable(nil)
	valuesBufPool.Put(vb)

	dst = encoding.MarshalUint32(dst, uint32(len(blockBuf)))
	dst = append(dst, blockBuf...)

	return dst, nil
}

type valuesBuf struct {
	b []int64
}

var valuesBufPool = sync.Pool{
	New: func() any {
		return &valuesBuf{}
	},
}
