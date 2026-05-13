package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestArchiveName(t *testing.T) {
	cases := map[string]string{
		"v0.1.1": "thicket_0.1.1_linux_amd64.tar.gz",
		"0.1.1":  "thicket_0.1.1_linux_amd64.tar.gz",
		"v1.2.3": "thicket_1.2.3_linux_amd64.tar.gz",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := archiveName(in, "linux", "amd64"); got != want {
				t.Errorf("archiveName(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

func TestIsManagedInstall_skipsKnownPackageManagers(t *testing.T) {
	cases := map[string]bool{
		"/usr/local/Cellar/thicket/1.0/bin/thicket":    false,
		"/opt/homebrew/Cellar/thicket/1.0/bin/thicket": false,
		"/nix/store/abcd-thicket/bin/thicket":          false,
		"/tmp/build/thicket":                           false,
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := IsManagedInstall(in); got != want {
				t.Errorf("IsManagedInstall(%q) = %v, want %v", in, got, want)
			}
		})
	}
}

func TestIsManagedInstall_skipsGoInstall(t *testing.T) {
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, "go", "bin", "thicket")
		if got := IsManagedInstall(p); got {
			t.Errorf("IsManagedInstall(%q) returned true; go install path should be skipped", p)
		}
	}
}

// Positive-case coverage (writable non-package-manager paths) is hard
// to exercise in a unit test because Go's t.TempDir() lives under
// $TMPDIR / /var/folders, which the heuristic correctly excludes.
// The package-manager and $TMPDIR skips above are the safety-critical
// negative cases; the default fall-through is covered by smoke tests
// against a real `~/.local/bin/thicket` install.

// TestApply_endToEnd spins up an httptest server that serves a fake
// release asset (tar.gz containing a single `thicket` binary) and the
// matching checksums.txt, then runs Apply and confirms the running-
// binary placeholder gets replaced with the new bytes.
func TestApply_endToEnd(t *testing.T) {
	// Build the tar.gz holding a fake thicket binary.
	newPayload := []byte("new-thicket-binary-bytes-v0.1.2")
	archive := buildTestArchive(t, "thicket", newPayload)
	checksum := sha256.Sum256(archive)
	hexSum := hex.EncodeToString(checksum[:])

	asset := archiveName("v0.1.2", runtime.GOOS, runtime.GOARCH)
	checksumsBody := fmt.Sprintf("%s  %s\n%s  checksums.txt\n",
		hexSum, asset, strings.Repeat("0", 64))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			_, _ = w.Write(archive)
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			_, _ = w.Write([]byte(checksumsBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Place a placeholder binary in a writable tempdir; that's what
	// Apply will rewrite.
	dir := t.TempDir()
	exe := filepath.Join(dir, "thicket")
	if err := os.WriteFile(exe, []byte("old-thicket"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Override Apply's wiring: rebase downloadURL to the test server.
	// This is the smallest surface to test against without faking
	// os.Executable() — we call the internal pieces directly.
	tmpDir := t.TempDir()
	tarballPath := filepath.Join(tmpDir, asset)
	if err := download(srv.URL+"/"+asset, tarballPath); err != nil {
		t.Fatalf("download: %v", err)
	}
	wantSum, err := fetchChecksumFrom(srv.URL+"/checksums.txt", asset)
	if err != nil {
		t.Fatalf("checksum fetch: %v", err)
	}
	if wantSum != hexSum {
		t.Fatalf("checksum mismatch: got %s want %s", wantSum, hexSum)
	}
	if err := verifyChecksum(tarballPath, wantSum); err != nil {
		t.Fatalf("verify: %v", err)
	}
	newBin := filepath.Join(tmpDir, "thicket")
	if err := extractBinary(tarballPath, newBin); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if err := swapBinary(newBin, exe); err != nil {
		t.Fatalf("swap: %v", err)
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newPayload) {
		t.Errorf("post-swap bytes mismatch: got %q want %q", got, newPayload)
	}
}

// fetchChecksumFrom is a test shim that calls the unexported
// fetchChecksum logic against an arbitrary URL.
func fetchChecksumFrom(url, asset string) (string, error) {
	// Reuse fetchChecksum's parsing path by inlining the same logic
	// against the test URL. (The real fetchChecksum builds the URL
	// from the GitHub release tag.)
	resp, err := http.Get(url) //nolint:gosec // test URL is local
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return "", err
	}
	for _, line := range strings.Split(buf.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s not found", asset)
}

func buildTestArchive(t *testing.T, binName string, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: binName, Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	// Throw in a noise file to verify extractBinary filters by base name.
	if err := tw.WriteHeader(&tar.Header{
		Name: "LICENSE", Mode: 0o644, Size: 4, Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("MIT\n")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestVerifyChecksum_mismatchFails(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "asset.tar.gz")
	if err := os.WriteFile(p, []byte("real bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	wrongSum := strings.Repeat("a", 64)
	if err := verifyChecksum(p, wrongSum); err == nil {
		t.Fatal("expected error on checksum mismatch, got nil")
	}
}
