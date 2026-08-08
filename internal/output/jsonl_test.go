package output

import (
	"fmt"
	"runtime"
	"syscall"
	"testing"
)

func TestIsClosedPipe(t *testing.T) {
	if !IsClosedPipe(fmt.Errorf("write result: %w", syscall.EPIPE)) {
		t.Fatal("IsClosedPipe() did not recognize wrapped EPIPE")
	}
	if runtime.GOOS == "windows" && !IsClosedPipe(fmt.Errorf("write result: %w", syscall.Errno(232))) {
		t.Fatal("IsClosedPipe() did not recognize Windows ERROR_NO_DATA")
	}
}
