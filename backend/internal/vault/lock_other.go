//go:build !unix

package vault

import "os"

// tryLock is advisory-only on platforms without flock: the PID file still
// records the owner but two processes are not prevented from opening.
func tryLock(*os.File) error { return nil }

func unlock(*os.File) error { return nil }
