package browsertest

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"
)

const profileCleanupTimeout = 2 * time.Second

// removeProfile runs after the browser and allocator have shut down. Chromium
// can still finish profile writes while RemoveAll walks the directory; retry
// that ENOTEMPTY race, not permission errors or other filesystem failures.
// A persistent ENOTEMPTY fails when the retry budget expires; an individual
// filesystem operation is synchronous and cannot be interrupted by ctx.
// The remove seam lets tests arrange the failing rmdir without a timing race.
func removeProfile(ctx context.Context, path string, remove func(string) error) error {
	for {
		err := remove(path)
		if !errors.Is(err, syscall.ENOTEMPTY) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("browser profile cleanup did not settle: %w", errors.Join(ctx.Err(), err))
		case <-time.After(10 * time.Millisecond):
		}
	}
}
