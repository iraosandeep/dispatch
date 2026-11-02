// Package updatetest fakes GitHub release downloads for tests.
package updatetest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const Repo = "acme/dispatch"

// Release describes the fake latest release.
type Release struct {
	Tag    string // "" means no release is published
	GOOS   string
	GOARCH string
	Binary []byte
	// CorruptChecksum publishes a wrong checksum for the archive.
	CorruptChecksum bool
}

// NewServer serves Release the way github.com does.
func NewServer(t testing.TB, rel Release) *httptest.Server {
	t.Helper()
	ver := strings.TrimPrefix(rel.Tag, "v")
	var name string
	var archive []byte
	if rel.GOOS == "windows" {
		name = fmt.Sprintf("dispatch_%s_%s_%s.zip", ver, rel.GOOS, rel.GOARCH)
		archive = zipArchive(t, "dispatch.exe", rel.Binary)
	} else {
		name = fmt.Sprintf("dispatch_%s_%s_%s.tar.gz", ver, rel.GOOS, rel.GOARCH)
		archive = tarGz(t, "dispatch", rel.Binary)
	}
	sum := sha256.Sum256(archive)
	checksum := hex.EncodeToString(sum[:])
	if rel.CorruptChecksum {
		checksum = strings.Repeat("0", 64)
	}
	sums := fmt.Sprintf("%s  %s\n%s  dispatch_%s_other_arch.tar.gz\n", checksum, name, strings.Repeat("1", 64), ver)

	prefix := "/" + Repo + "/releases"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == prefix+"/latest":
			if rel.Tag == "" {
				http.Redirect(w, r, prefix, http.StatusFound)
				return
			}
			http.Redirect(w, r, prefix+"/tag/"+rel.Tag, http.StatusFound)
		case r.URL.Path == prefix+"/download/"+rel.Tag+"/checksums.txt":
			w.Write([]byte(sums))
		case r.URL.Path == prefix+"/download/"+rel.Tag+"/"+name:
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func tarGz(t testing.TB, name string, data []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		data []byte
	}{{"README.md", []byte("readme")}, {name, data}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(f.data)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipArchive(t testing.TB, name string, data []byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(data)
	zw.Close()
	return buf.Bytes()
}
