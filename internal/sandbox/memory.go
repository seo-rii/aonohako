package sandbox

import (
	"runtime"
	"runtime/debug"
)

// TrimParentMemoryBeforeSandbox returns idle Go heap pages before starting a
// sandbox child so container-level memory limits have more headroom.
func TrimParentMemoryBeforeSandbox() {
	runtime.GC()
	debug.FreeOSMemory()
}
