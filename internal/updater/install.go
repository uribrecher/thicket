// Self-install (replace the running binary) for thicket. Mirrors the
// install.sh logic in pure Go: pull the release tarball + checksums,
// verify SHA-256, extract the thicket binary, and atomically rename
// it over the currently-running executable.
package updater

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrUnmanagedInstall signals that the current executable lives
// somewhere that auto-update should not touch (Go install bin,
// Homebrew cellar, Nix store, source build).
var ErrUnmanagedInstall = errors.New("not a managed thicket install")

// archiveName mirrors the install.sh / goreleaser naming convention:
//
//	thicket_<semver>_<os>_<arch>.tar.gz
//
// goreleaser strips the leading "v" so we do the same.
func archiveName(version, goos, goarch string) string {
	return fmt.Sprintf("thicket_%s_%s_%s.tar.gz",
		strings.TrimPrefix(version, "v"), goos, goarch)
}

// downloadURL returns the GitHub releases asset URL for a given
// release tag and asset filename.
func downloadURL(tag, asset string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s",
		repoSlug, tag, asset)
}

// Apply downloads the asset for `tag` matching this binary's
// GOOS/GOARCH, verifies its SHA-256 against the release's
// checksums.txt, extracts the `thicket` binary, and atomically swaps
// it over the currently-running executable. Returns ErrUnmanagedInstall
// when the current binary path looks externally-managed (go install,
// brew, nix, source build, $TMPDIR). On any other failure the
// currently-installed binary is preserved.
func Apply(tag string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve current binary: %w", err)
	}
	if !IsManagedInstall(exe) {
		return ErrUnmanagedInstall
	}

	tmpDir, err := os.MkdirTemp("", "thicket-update-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	asset := archiveName(tag, runtime.GOOS, runtime.GOARCH)
	tarballPath := filepath.Join(tmpDir, asset)
	if err := download(downloadURL(tag, asset), tarballPath); err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}

	wantSum, err := fetchChecksum(tag, asset)
	if err != nil {
		return fmt.Errorf("fetch checksum: %w", err)
	}
	if err := verifyChecksum(tarballPath, wantSum); err != nil {
		return fmt.Errorf("verify %s: %w", asset, err)
	}

	newBin := filepath.Join(tmpDir, "thicket")
	if err := extractBinary(tarballPath, newBin); err != nil {
		return fmt.Errorf("extract %s: %w", asset, err)
	}
	if err := os.Chmod(newBin, 0o755); err != nil {
		return err
	}
	return swapBinary(newBin, exe)
}

// IsManagedInstall is true when the current executable lives somewhere
// auto-update can safely write to and is NOT under a package manager's
// tree. The path is expected to already be symlink-resolved.
func IsManagedInstall(exePath string) bool {
	skipPrefixes := []string{
		"/usr/local/Cellar/",
		"/opt/homebrew/Cellar/",
		"/nix/store/",
		"/var/folders/", // macOS $TMPDIR
		"/tmp/",
		filepath.Join(os.TempDir()) + string(filepath.Separator),
	}
	for _, p := range skipPrefixes {
		if strings.HasPrefix(exePath, p) {
			return false
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		// Source builds and cached intermediates: explicitly skip.
		for _, sub := range []string{"/.cache/", "/Library/Caches/"} {
			if strings.HasPrefix(exePath, filepath.Join(home, sub)) {
				return false
			}
		}
	}
	// Heuristic for `go install` users: anything under a `go/bin` dir
	// is owned by the Go toolchain — don't touch.
	dir := filepath.Dir(exePath)
	base := filepath.Base(dir)
	parent := filepath.Base(filepath.Dir(dir))
	if base == "bin" && parent == "go" {
		return false
	}
	// Otherwise: check the parent dir is writable. If we can't even
	// rename a temp file into it we can't do an atomic swap, and the
	// user almost certainly has a system-managed install (e.g. owned
	// by root in /usr/bin).
	probe := filepath.Join(dir, ".thicket-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return true
}

func download(url, dest string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "thicket-self-update")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, resp.Body)
	return err
}

// fetchChecksum pulls the release's checksums.txt and returns the
// hex SHA-256 for the named asset. checksums.txt format (one per line):
//
//	<hex>  <filename>
func fetchChecksum(tag, asset string) (string, error) {
	url := downloadURL(tag, "checksums.txt")
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "thicket-self-update")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[1] == asset {
			return fields[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("checksum for %s not found", asset)
}

func verifyChecksum(path, wantHex string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, wantHex) {
		return fmt.Errorf("sha256 mismatch: got %s want %s", got, wantHex)
	}
	return nil
}

// extractBinary pulls only the "thicket" file out of the tar.gz at
// `archive` and writes it to `dest`. Everything else (LICENSE,
// README.md, CHANGELOG.md) is skipped.
func extractBinary(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("thicket binary not found in archive")
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) != "thicket" {
			continue
		}
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		// G110 (decompression bomb) is OK here: it's our own release
		// archive, verified by SHA-256 against checksums.txt above.
		if _, err := io.Copy(out, tr); err != nil { //nolint:gosec
			_ = out.Close()
			return err
		}
		return out.Close()
	}
}

// swapBinary atomically replaces the running binary at `target` with
// the file at `src`. On the same filesystem this is os.Rename. If
// they're on different filesystems (e.g. /tmp vs /home), we fall back
// to copy-into-target-dir-then-rename so the final swap is still
// atomic on the destination FS.
func swapBinary(src, target string) error {
	if err := os.Rename(src, target); err == nil {
		return nil
	}
	// Cross-fs fallback: copy src to a sibling of target, then rename.
	staging := target + ".new"
	if err := copyFile(src, staging); err != nil {
		return err
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		_ = os.Remove(staging)
		return err
	}
	if err := os.Rename(staging, target); err != nil {
		_ = os.Remove(staging)
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
