package update_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iraosandeep/dispatch/internal/update"
	"github.com/iraosandeep/dispatch/internal/update/updatetest"
)

func newUpdater(url, goos string) *update.Updater {
	return &update.Updater{Repo: updatetest.Repo, BaseURL: url, GOOS: goos, GOARCH: "arm64"}
}

func TestLatest(t *testing.T) {
	srv := updatetest.NewServer(t, updatetest.Release{Tag: "v1.4.0", GOOS: "linux", GOARCH: "arm64", Binary: []byte("x")})
	tag, err := newUpdater(srv.URL, "linux").Latest(context.Background())
	if err != nil || tag != "v1.4.0" {
		t.Fatalf("Latest = %q, %v", tag, err)
	}
}

func TestLatestNoRelease(t *testing.T) {
	srv := updatetest.NewServer(t, updatetest.Release{})
	_, err := newUpdater(srv.URL, "linux").Latest(context.Background())
	if !errors.Is(err, update.ErrNoRelease) {
		t.Fatalf("got %v", err)
	}
}

func TestDownloadVerifiesAndExtracts(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			srv := updatetest.NewServer(t, updatetest.Release{Tag: "v1.4.0", GOOS: goos, GOARCH: "arm64", Binary: []byte("new-binary")})
			bin, err := newUpdater(srv.URL, goos).Download(context.Background(), "v1.4.0")
			if err != nil || string(bin) != "new-binary" {
				t.Fatalf("Download = %q, %v", bin, err)
			}
		})
	}
}

func TestDownloadRejectsBadChecksum(t *testing.T) {
	srv := updatetest.NewServer(t, updatetest.Release{Tag: "v1.4.0", GOOS: "linux", GOARCH: "arm64", Binary: []byte("evil"), CorruptChecksum: true})
	_, err := newUpdater(srv.URL, "linux").Download(context.Background(), "v1.4.0")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("got %v", err)
	}
}

func TestDownloadMissingPlatform(t *testing.T) {
	srv := updatetest.NewServer(t, updatetest.Release{Tag: "v1.4.0", GOOS: "linux", GOARCH: "arm64", Binary: []byte("x")})
	u := newUpdater(srv.URL, "freebsd")
	if _, err := u.Download(context.Background(), "v1.4.0"); err == nil || !strings.Contains(err.Error(), "no build for freebsd") {
		t.Fatalf("got %v", err)
	}
}

func TestInstallReplacesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dispatch")
	os.WriteFile(path, []byte("old"), 0o755)
	if err := update.Install(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("content = %q", got)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o755 {
			t.Errorf("mode = %o", info.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("leftover temp files: %v", entries)
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.1.0", "v0.1.0", 0},
		{"v0.1.0", "v0.2.0", -1},
		{"v1.10.0", "v1.9.9", 1},
		{"v1.0.0-rc.1", "v1.0.0", -1},
		{"v0.0.0-20261030124125-ec85fc4aa764", "v0.1.0", -1}, // go install pseudo-version
		{"v1.0.0+build", "v1.0.0", 0},
		{"dev", "v0.1.0", -1},
	}
	for _, tt := range tests {
		if got := update.Compare(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%q, %q) = %d; want %d", tt.a, tt.b, got, tt.want)
		}
	}
	if update.IsVersion("ec85fc4-dirty") || !update.IsVersion("v1.2.3") {
		t.Error("IsVersion")
	}
}

func TestManagedBy(t *testing.T) {
	if update.ManagedBy("/opt/homebrew/Cellar/dispatch/1.0.0/bin/dispatch") == "" {
		t.Error("Homebrew path not detected")
	}
	if update.ManagedBy("/Users/x/.local/bin/dispatch") != "" {
		t.Error("false positive")
	}
}
