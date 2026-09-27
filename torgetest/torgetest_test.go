package torgetest_test

import (
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge/torgetest"
)

// TestLoggingAfterTestEnds covers handlers that outlive their test, such as
// those on hijacked connections: logging from them while the test finishes
// must not race with the testing package (run with -race).
func TestLoggingAfterTestEnds(t *testing.T) {
	stop, stopped := make(chan struct{}), make(chan struct{})
	t.Run("inner", func(t *testing.T) {
		logger := torgetest.NewApp(t).Logger()
		started := make(chan struct{})
		go func() {
			defer close(stopped)
			close(started)
			for {
				select {
				case <-stop:
					return
				default:
					logger.Info("still running")
				}
			}
		}()
		<-started
	})
	time.Sleep(20 * time.Millisecond)
	close(stop)
	<-stopped
}
