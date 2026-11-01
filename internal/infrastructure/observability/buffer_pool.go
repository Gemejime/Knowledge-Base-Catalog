package observability

import (
	"bytes"
	"sync"
)

const (
	defaultBufferSize = 4096
	maxPooledCapacity = 64 * 1024 // 64 KiB cap to prevent retaining oversized buffers in RSS (<= 128 MiB SLO)
)

var jsonBufferPool = sync.Pool{
	New: func() any {
		return bytes.NewBuffer(make([]byte, 0, defaultBufferSize))
	},
}

// AcquireBuffer retrieves a reset *bytes.Buffer from the shared sync.Pool.
func AcquireBuffer() *bytes.Buffer {
	buf := jsonBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

// ReleaseBuffer resets and returns a *bytes.Buffer to the sync.Pool if its capacity is within bounds.
func ReleaseBuffer(buf *bytes.Buffer) {
	if buf == nil || buf.Cap() > maxPooledCapacity {
		return
	}
	buf.Reset()
	jsonBufferPool.Put(buf)
}
