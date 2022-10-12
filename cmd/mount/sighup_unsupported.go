//go:build plan9 || js
// +build plan9 js

package mount

import (
	"os"
)

// NotifyOnSigHup makes SIGHUP notify given channel on supported systems
func NotifyOnSigHup(sighupChan chan os.Signal) {}
