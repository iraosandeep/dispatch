// Package update replaces the running dispatch binary with a published
// GitHub release, verifying the release checksum first.
package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// DefaultRepo is the GitHub repository that publishes releases.
const DefaultRepo = "iraosandeep/dispatch"

const maxArchiveBytes = 200 << 20

// ErrNoRelease means the repository has no published release.
var ErrNoRelease = errors.New("no release has been published yet")

// Updater finds and downloads releases. Zero values select defaults.
type Updater struct {
	Repo    string // owner/name
	BaseURL string // default https://github.com
	HTTP    *http.Client
	GOOS    string
	GOARCH  string
}

func (u *Updater) repo() string { return valueOr(u.Repo, DefaultRepo) }
func (u *Updater) baseURL() string {
	return strings.TrimRight(valueOr(u.BaseURL, "https://github.com"), "/")
}
func (u *Updater) goos() string   { return valueOr(u.GOOS, runtime.GOOS) }
func (u *Updater) goarch() string { return valueOr(u.GOARCH, runtime.GOARCH) }

func (u *Updater) client() *http.Client {
	if u.HTTP != nil {
		return u.HTTP
	}
	return http.DefaultClient
}

// Latest returns the newest release tag, e.g. "v0.2.0". It follows GitHub's
// /releases/latest redirect rather than calling the rate-limited API.
func (u *Updater) Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.baseURL()+"/"+u.repo()+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	noRedirect := *u.client()
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := noRedirect.Do(req)
	if err != nil {
		return "", fmt.Errorf("check for updates: %w", err)
	}
	resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", ErrNoRelease
	case resp.StatusCode < 300 || resp.StatusCode >= 400:
		return "", fmt.Errorf("check for updates: unexpected HTTP %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	tag := loc[strings.LastIndex(loc, "/")+1:]
	if !strings.Contains(loc, "/releases/tag/") || !IsVersion(tag) {
		// /releases/latest redirects to /releases when nothing is published.
		return "", ErrNoRelease
	}
	return tag, nil
}

// ArchiveName returns the release asset name for tag on this platform.
func (u *Updater) ArchiveName(tag string) string {
	ext := "tar.gz"
	if u.goos() == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("dispatch_%s_%s_%s.%s", strings.TrimPrefix(tag, "v"), u.goos(), u.goarch(), ext)
}

// Download fetches the release archive for tag, verifies it against the
// release's checksums.txt, and returns the extracted binary.
func (u *Updater) Download(ctx context.Context, tag string) ([]byte, error) {
	base := u.baseURL() + "/" + u.repo() + "/releases/download/" + tag + "/"
	name := u.ArchiveName(tag)

	sums, err := u.get(ctx, base+"checksums.txt", 1<<20)
	if err != nil {
		return nil, fmt.Errorf("download checksums: %w", err)
	}
	want, ok := findChecksum(sums, name)
	if !ok {
		return nil, fmt.Errorf("release %s has no build for %s/%s", tag, u.goos(), u.goarch())
	}

	archive, err := u.get(ctx, base+name, maxArchiveBytes)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return nil, fmt.Errorf("checksum mismatch for %s: refusing to install", name)
	}

	binName := "dispatch"
	if u.goos() == "windows" {
		binName += ".exe"
		return extractZip(archive, binName)
	}
	return extractTarGz(archive, binName)
}

func (u *Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response larger than %d bytes", limit)
	}
	return data, nil
}

func findChecksum(sums []byte, name string) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

func extractTarGz(archive []byte, binName string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("archive does not contain %s", binName)
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == binName {
			return io.ReadAll(io.LimitReader(tr, maxArchiveBytes))
		}
	}
}

func extractZip(archive []byte, binName string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) == binName {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, maxArchiveBytes))
		}
	}
	return nil, fmt.Errorf("archive does not contain %s", binName)
}

// Install atomically replaces the executable at path with bin.
func Install(path string, bin []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".dispatch-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}

	if runtime.GOOS == "windows" {
		// A running .exe can't be overwritten, but it can be renamed.
		old := path + ".old"
		_ = os.Remove(old)
		if err := os.Rename(path, old); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), path)
}

// ManagedBy reports a package manager that owns the binary at path, if any.
func ManagedBy(path string) string {
	p := filepath.ToSlash(path)
	if strings.Contains(p, "/Cellar/") || strings.Contains(p, "/homebrew/") || strings.Contains(p, "/linuxbrew/") {
		return "Homebrew (run: brew upgrade dispatch)"
	}
	return ""
}

// IsVersion reports whether v looks like a semantic version (optional "v").
func IsVersion(v string) bool {
	_, ok := parse(v)
	return ok
}

// Compare compares semantic versions a and b, returning -1, 0, or +1.
// A pre-release (v1.0.0-rc.1) sorts before its release (v1.0.0).
// Invalid versions sort before valid ones.
func Compare(a, b string) int {
	va, okA := parse(a)
	vb, okB := parse(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for i := 0; i < 3; i++ {
		if va.nums[i] != vb.nums[i] {
			if va.nums[i] < vb.nums[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case va.pre == vb.pre:
		return 0
	case va.pre == "":
		return 1
	case vb.pre == "":
		return -1
	case va.pre < vb.pre:
		return -1
	default:
		return 1
	}
}

type version struct {
	nums [3]int
	pre  string
}

func parse(v string) (version, bool) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i] // drop build metadata
	}
	core, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var out version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return version{}, false
		}
		out.nums[i] = n
	}
	out.pre = pre
	return out, true
}

func valueOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
