// Package output renders stable machine-readable and human-readable results.
package output

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"syscall"
)

// WriteJSONLine writes one JSON value followed by a newline.
func WriteJSONLine(w io.Writer, value any) error {
	return json.NewEncoder(w).Encode(value)
}

// IsClosedPipe reports the normal condition where a downstream command such
// as head stops reading before PanFind has emitted every result.
func IsClosedPipe(err error) bool {
	if errors.Is(err, syscall.EPIPE) || errors.Is(err, os.ErrClosed) {
		return true
	}
	if runtime.GOOS == "windows" {
		// ERROR_BROKEN_PIPE and ERROR_NO_DATA are both returned when a
		// downstream process closes a Windows pipe early.
		return errors.Is(err, syscall.Errno(109)) || errors.Is(err, syscall.Errno(232))
	}
	return false
}
