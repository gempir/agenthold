package dl

import "os"

// openSelf opens the running executable image. /proc/self/exe keeps working when an upgrade has
// replaced the file on disk, and then still serves the version this process runs.
func openSelf() (*os.File, error) { return os.Open("/proc/self/exe") }
