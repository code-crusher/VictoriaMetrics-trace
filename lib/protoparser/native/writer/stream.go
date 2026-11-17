package writer

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"sync"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/encoding"
)

// defaultFlushSize is the size of the buffer, which is flushed to w
// when it is full.
const defaultFlushSize = 64 * 1024

// Stream writes blocks in native ingestion protocol format to the underlying
// writer.
//
// Usage:
//
//	s, err := NewStream(w, minTimestamp, maxTimestamp)
//	if err != nil {
//		// handle error
//	}
//	for _, b := range blocks {
//		if err := s.WriteBlock(b.MetricName, b.Timestamps, b.Values, 64); err != nil {
//			// handle error
//			break
//		}
//	}
//	if err := s.Close(); err != nil {
//		// handle error
//	}
//
// Stream must be closed after use. It is not safe for concurrent use.
type Stream struct {
	// bw buffers the written data before flushing it to the underlying writer.
	bw *bufio.Writer

	// err contains the first error occurred during writing to the stream.
	err error

	// headerBuf is a reusable buffer for marshaling the stream header.
	headerBuf []byte
}

// NewStream returns a new Stream for writing blocks in native ingestion
// protocol format to w.
//
// The stream header with the given [minTimestamp, maxTimestamp] time range
// (in milliseconds) is written to w immediately. The server uses this time
// range for filtering out samples with timestamps outside the range, so it
// must cover all the blocks written to the stream. It is safe to pass a wider
// time range than the actual samples' time range - samples outside the
// passed blocks are simply not affected.
//
// The returned Stream must be closed after use.
func NewStream(w io.Writer, minTimestamp, maxTimestamp int64) (*Stream, error) {
	if minTimestamp > maxTimestamp {
		return nil, fmt.Errorf("minTimestamp=%d cannot exceed maxTimestamp=%d", minTimestamp, maxTimestamp)
	}
	s := getStream()
	s.bw.Reset(w)
	s.writeHeader(minTimestamp, maxTimestamp)
	return s, nil
}

func getStream() *Stream {
	v := streamPool.Get()
	if v == nil {
		return &Stream{
			bw: bufio.NewWriterSize(nil, defaultFlushSize),
		}
	}
	return v.(*Stream)
}

// PutStream returns s to the pool, so it can be reused by NewStream.
//
// PutStream flushes all the pending data to the underlying writer and
// returns the flush error (if any), so PutStream can be used instead of
// Close when the stream is no longer needed.
func PutStream(s *Stream) error {
	err := s.Close()
	s.bw.Reset(nil)
	s.err = nil
	s.headerBuf = s.headerBuf[:0]
	streamPool.Put(s)
	return err
}

// Close flushes all the pending data to the underlying writer and finishes
// the stream.
func (s *Stream) Close() error {
	if s.err != nil {
		return s.err
	}
	if err := s.bw.Flush(); err != nil {
		s.err = err
		return err
	}
	return nil
}

// MustClose is like Close, but panics on error.
func (s *Stream) MustClose() {
	if err := s.Close(); err != nil {
		panic(fmt.Errorf("cannot close native writer stream: %w", err))
	}
}

// writeHeader writes the stream header with the given time range to the
// stream.
func (s *Stream) writeHeader(minTimestamp, maxTimestamp int64) {
	// The server uses the time range from the header for filtering out
	// samples with timestamps outside the range.
	s.headerBuf = encoding.MarshalInt64(s.headerBuf[:0], minTimestamp)
	s.headerBuf = encoding.MarshalInt64(s.headerBuf, maxTimestamp)
	s.bw.Write(s.headerBuf)
}

// WriteBlock writes a single block with the given samples for the given
// metricName to the stream.
//
// The samples with timestamps outside the time range passed to NewStream
// are dropped by the server during importing.
//
// metricName must contain the metric name in `metric{tag1="value1",...}` form.
// See storage.MetricName for details.
//
// timestamps must contain non-decreasing values, i.e. the samples in the
// block must be sorted by timestamp.
//
// precisionBits must be in the range [1...64], where 1 means 50% precision,
// while 64 means 100% precision, i.e. lossless encoding. It is recommended
// to use 64 as the default value, since it provides lossless encoding,
// which is suitable for the most of cases.
func (s *Stream) WriteBlock(metricName string, timestamps []int64, values []float64, precisionBits uint8) error {
	if s.err != nil {
		return s.err
	}
	if len(timestamps) == 0 {
		return nil
	}

	bb := getBlockBuf()
	var err error
	bb.b, err = marshalBlock(bb.b[:0], metricName, timestamps, values, precisionBits)
	if err == nil {
		_, err = s.bw.Write(bb.b)
	} else {
		s.err = err
	}
	putBlockBuf(bb)
	return err
}

var streamPool sync.Pool

type blockBuf struct {
	b []byte
}

var blockBufPool = sync.Pool{
	New: func() any {
		return &blockBuf{}
	},
}

func getBlockBuf() *blockBuf {
	return blockBufPool.Get().(*blockBuf)
}

func putBlockBuf(bb *blockBuf) {
	bb.b = bb.b[:0]
	blockBufPool.Put(bb)
}

// MinTimestamp is the minimum timestamp, which can be used in the stream
// time range. Timestamps are in milliseconds.
const MinTimestamp int64 = 0

// MaxTimestamp is the maximum timestamp, which can be used in the stream
// time range. Timestamps are in milliseconds.
const MaxTimestamp int64 = math.MaxInt64 / 1_000_000
