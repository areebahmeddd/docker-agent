// Package atomicfile writes files atomically (write-to-temp + rename)
// with a configurable file mode.
//
// It wraps [github.com/natefinch/atomic], which performs the atomic
// rename but does not let the caller specify a permission bitmask on
// the resulting file. [Write] addresses that gap by chmod-ing the file
// after the rename, so the same call site can both publish the file
// atomically and ensure it is not world-readable.
package atomicfile

import (
	"io"
	"os"

	"github.com/natefinch/atomic"
)

// Write atomically writes data from r to path and sets the file's mode.
//
// The write goes to a temporary file in the same directory and is then
// renamed into place; readers therefore observe either the previous
// contents or the new contents, never a partial write. The chmod is
// applied after the rename: callers that care about secrecy should
// avoid having a third party already holding an open descriptor on
// path before the call.
func Write(path string, r io.Reader, mode os.FileMode) error {
	if err := atomic.WriteFile(path, r); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}
