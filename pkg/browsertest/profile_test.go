package browsertest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

func TestProfileCleanupToleratesALateFile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		profile := filepath.Join(t.TempDir(), "profile")
		leaf := filepath.Join(profile, "Default")
		if err := os.MkdirAll(leaf, 0700); err != nil {
			t.Fatal(err)
		}
		attempts := 0
		remove := func(path string) error {
			attempts++
			if attempts == 1 {
				// Reproduce the failing leaf rmdir: enumeration saw an empty
				// directory, then Chrome's last write arrived before its removal.
				entries, err := os.ReadDir(leaf)
				if err != nil || len(entries) != 0 {
					t.Fatalf("initial leaf=%v err=%v", entries, err)
				}
				if err := os.WriteFile(filepath.Join(leaf, "late-file"), []byte("shutdown state"), 0600); err != nil {
					t.Fatal(err)
				}
				err = os.Remove(leaf)
				if !errors.Is(err, syscall.ENOTEMPTY) {
					t.Fatalf("late write did not produce ENOTEMPTY: %v", err)
				}
				return err
			}
			return os.RemoveAll(path)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := removeProfile(ctx, profile, remove); err != nil {
			t.Fatalf("cleanup after a late profile write: %v", err)
		}
		if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("profile remains after cleanup: %v", err)
		}
		if attempts != 2 {
			t.Fatalf("cleanup attempts=%d, want the failed removal followed by success", attempts)
		}
	})
}

func TestProfileCleanupReportsAPersistentWriter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 35*time.Millisecond)
		defer cancel()
		path := "profile/Default"
		attempts := 0
		start := time.Now()
		err := removeProfile(ctx, "profile", func(string) error {
			attempts++
			return &os.PathError{Op: "unlinkat", Path: path, Err: syscall.ENOTEMPTY}
		})
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, syscall.ENOTEMPTY) || !strings.Contains(err.Error(), path) {
			t.Fatalf("cleanup lost deadline or filesystem failure: %v", err)
		}
		if elapsed := time.Since(start); elapsed != 35*time.Millisecond {
			t.Fatalf("cleanup ran for %s, want its deadline", elapsed)
		}
		if attempts < 2 {
			t.Fatalf("did not retry a transient failure: %d attempts", attempts)
		}
	})
}

func TestProfileCleanupDoesNotRetryOtherErrors(t *testing.T) {
	for _, cause := range []error{nil, syscall.EACCES, syscall.EPERM, syscall.EIO} {
		synctest.Test(t, func(t *testing.T) {
			attempts := 0
			start := time.Now()
			err := removeProfile(t.Context(), "profile", func(string) error { attempts++; return cause })
			if !errors.Is(err, cause) || attempts != 1 || time.Since(start) != 0 {
				t.Fatalf("cause=%v result=%v attempts=%d elapsed=%s", cause, err, attempts, time.Since(start))
			}
		})
	}
}

func TestNewBrowserCleansProfileAfterShutdown(t *testing.T) {
	var cleanups []func()
	cleanup := func() {
		for _, f := range slices.Backward(cleanups) {
			f()
		}
		cleanups = nil
	}
	ctx, err := newBrowser(t, func(f func()) { cleanups = append(cleanups, f) })
	t.Cleanup(cleanup)
	if err != nil {
		t.Fatal(err)
	}
	args, err := browser.GetBrowserCommandLine().Do(cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser))
	if err != nil {
		t.Fatal(err)
	}
	var profile string
	for _, arg := range args {
		if path, ok := strings.CutPrefix(arg, "--user-data-dir="); ok {
			profile = path
		}
	}
	if profile == "" {
		t.Fatal("browser command line contains no profile")
	}
	if _, err := os.Stat(profile); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("browser cleanup returned with its profile still present: %v", err)
	}
}
