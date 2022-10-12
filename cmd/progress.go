// Show the dynamic progress bar

package cmd

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ThierryZhou/go-s3fs/fs"
	"github.com/ThierryZhou/go-s3fs/fs/log"
	"github.com/ThierryZhou/go-s3fs/fs/operations"
)

const (
	// interval between progress prints
	defaultProgressInterval = 500 * time.Millisecond
	// time format for logging
	logTimeFormat = "2006-01-02 15:04:05"
)

// startProgress starts the progress bar printing
//
// It returns a func which should be called to stop the stats.
func startProgress() func() {
	stopStats := make(chan struct{})
	oldLogPrint := fs.LogPrint
	oldSyncPrint := operations.SyncPrintf

	if !log.Redirected() {
		// Intercept the log calls if not logging to file or syslog
		fs.LogPrint = func(level fs.LogLevel, text string) {
			printProgress(fmt.Sprintf("%s %-6s: %s", time.Now().Format(logTimeFormat), level, text))

		}
	}

	// Intercept output from functions such as HashLister to stdout
	operations.SyncPrintf = func(format string, a ...interface{}) {
		printProgress(fmt.Sprintf(format, a...))
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		progressInterval := defaultProgressInterval
		ticker := time.NewTicker(progressInterval)
		for {
			select {
			case <-ticker.C:
				printProgress("")
			case <-stopStats:
				ticker.Stop()
				printProgress("")
				fs.LogPrint = oldLogPrint
				operations.SyncPrintf = oldSyncPrint
				fmt.Println("")
				return
			}
		}
	}()
	return func() {
		close(stopStats)
		wg.Wait()
	}
}

// state for the progress printing
var (
	nlines     = 0 // number of lines in the previous stats block
	progressMu sync.Mutex
)

// printProgress prints the progress with an optional log
func printProgress(logMessage string) {
	progressMu.Lock()
	defer progressMu.Unlock()

	var buf bytes.Buffer
	logMessage = strings.TrimSpace(logMessage)

	out := func(s string) {
		buf.WriteString(s)
	}

	if logMessage != "" {
		out("\n")
	}

	if logMessage != "" {
		out(logMessage + "\n")
	}
}
