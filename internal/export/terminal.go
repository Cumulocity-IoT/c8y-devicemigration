package export

import (
	"io"
	"os"
)

// isTerminal reports whether a writer is a character device, which is the only
// place ANSI escapes belong.
func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
