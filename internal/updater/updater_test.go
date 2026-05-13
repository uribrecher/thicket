package updater

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/adrg/xdg"
)

// withTestEnv arranges:
//   - $XDG_CONFIG_HOME points at a fresh temp dir (per-subtest cache)
//   - apiBaseURL points at a caller-supplied httptest server
//   - THICKET_NO_UPDATE_CHECK is cleared so it doesn't accidentally
//     short-circuit the test
//
// The cleanup hook restores apiBaseURL and reloads adrg/xdg so other
// tests in the same package see the real env again.
func withTestEnv(t *testing.T, apiURL string) {
	t.Helper()
	t.Setenv("THICKET_NO_UPDATE_CHECK", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	xdg.Reload()
	prev := apiBaseURL
	apiBaseURL = apiURL
	t.Cleanup(func() {
		apiBaseURL = prev
		xdg.Reload()
	})
}

// stubLatestReleaseHandler returns an http.HandlerFunc that responds
// to GET /repos/.../releases/latest with the given tag_name. Other
// paths 404.
func stubLatestReleaseHandler(t *testing.T, tag string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": tag})
	}
}

func TestCheckOnRun_envDisableSkipsEverything(t *testing.T) {
	// Server must never be hit.
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		http.NotFound(w, nil)
	}))
	defer srv.Close()
	withTestEnv(t, srv.URL)
	t.Setenv("THICKET_NO_UPDATE_CHECK", "1")

	var out, errOut bytes.Buffer
	CheckOnRun("v0.1.0", &out, &errOut)

	if hit {
		t.Error("HTTP fired despite THICKET_NO_UPDATE_CHECK=1")
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("expected silent skip, got out=%q errOut=%q", out.String(), errOut.String())
	}
	if _, err := os.Stat(mustCachePath(t)); err == nil {
		t.Error("cache file written despite env disable")
	}
}

func TestCheckOnRun_devVersionSkips(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
	}))
	defer srv.Close()
	withTestEnv(t, srv.URL)

	var out, errOut bytes.Buffer
	CheckOnRun("v0.1.0-13-gabcd-dirty", &out, &errOut)

	if hit {
		t.Error("HTTP fired for a dev/dirty version")
	}
	if errOut.Len() != 0 {
		t.Errorf("expected silent skip, got errOut=%q", errOut.String())
	}
}

func TestCheckOnRun_alreadyLatestNoOp(t *testing.T) {
	srv := httptest.NewServer(stubLatestReleaseHandler(t, "v0.1.0"))
	defer srv.Close()
	withTestEnv(t, srv.URL)

	var out, errOut bytes.Buffer
	CheckOnRun("v0.1.0", &out, &errOut)

	if errOut.Len() != 0 {
		t.Errorf("expected no output when on latest, got %q", errOut.String())
	}
	// Cache should have been written so the next invocation can use it.
	st, err := loadCache()
	if err != nil {
		t.Fatalf("loadCache: %v", err)
	}
	if st.LatestVersion != "v0.1.0" {
		t.Errorf("cache LatestVersion = %q, want v0.1.0", st.LatestVersion)
	}
}

func TestCheckOnRun_newerAvailableHitsUnmanagedHint(t *testing.T) {
	// The test binary lives in $TMPDIR / /var/folders, which
	// IsManagedInstall correctly excludes. CheckOnRun should therefore
	// route through printUnmanagedHint and persist the declined
	// version so subsequent runs don't re-prompt.
	srv := httptest.NewServer(stubLatestReleaseHandler(t, "v0.2.0"))
	defer srv.Close()
	withTestEnv(t, srv.URL)

	var out, errOut bytes.Buffer
	CheckOnRun("v0.1.0", &out, &errOut)

	if !strings.Contains(errOut.String(), "v0.1.0 → v0.2.0") {
		t.Errorf("expected version hint in errOut, got %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "install.sh") {
		t.Errorf("expected install.sh hint in errOut, got %q", errOut.String())
	}
	st, err := loadCache()
	if err != nil {
		t.Fatalf("loadCache: %v", err)
	}
	if st.DeclinedVersion != "v0.2.0" {
		t.Errorf("DeclinedVersion = %q, want v0.2.0 (so we don't re-prompt for 24h)",
			st.DeclinedVersion)
	}
}

func TestCheckOnRun_declinedVersionStaysSilent(t *testing.T) {
	srv := httptest.NewServer(stubLatestReleaseHandler(t, "v0.2.0"))
	defer srv.Close()
	withTestEnv(t, srv.URL)

	// Pre-populate the cache as if the user already saw v0.2.0
	// within the current 24h window.
	if err := saveCache(cacheState{
		CheckedAt:       time.Now(),
		LatestVersion:   "v0.2.0",
		DeclinedVersion: "v0.2.0",
	}); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	CheckOnRun("v0.1.0", &out, &errOut)

	if errOut.Len() != 0 {
		t.Errorf("expected silent skip on declined+fresh cache, got %q", errOut.String())
	}
}

func TestCheckOnRun_networkErrorSoftFails(t *testing.T) {
	// Server returns 500 → fetchLatestTag errors → CheckOnRun
	// should soft-fail without panicking or printing anything.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	withTestEnv(t, srv.URL)

	var out, errOut bytes.Buffer
	CheckOnRun("v0.1.0", &out, &errOut)

	if errOut.Len() != 0 || out.Len() != 0 {
		t.Errorf("expected silent soft-fail, got out=%q errOut=%q",
			out.String(), errOut.String())
	}
}

func TestCheckAndApplyNow_alreadyLatestReturnsNil(t *testing.T) {
	srv := httptest.NewServer(stubLatestReleaseHandler(t, "v0.1.0"))
	defer srv.Close()
	withTestEnv(t, srv.URL)

	var out, errOut bytes.Buffer
	if err := CheckAndApplyNow("v0.1.0", &out, &errOut); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "already at the latest") {
		t.Errorf("expected 'already at the latest' message, got %q", out.String())
	}
}

func TestCheckAndApplyNow_devVersionErrors(t *testing.T) {
	srv := httptest.NewServer(stubLatestReleaseHandler(t, "v0.1.0"))
	defer srv.Close()
	withTestEnv(t, srv.URL)

	var out, errOut bytes.Buffer
	err := CheckAndApplyNow("v0.1.0-13-gabcd", &out, &errOut)
	if err == nil {
		t.Fatal("expected error for dev/dirty version, got nil")
	}
	if !strings.Contains(err.Error(), "tagged releases") {
		t.Errorf("error message should explain tagged-release limitation, got %v", err)
	}
}

func TestCheckAndApplyNow_networkErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	withTestEnv(t, srv.URL)

	var out, errOut bytes.Buffer
	err := CheckAndApplyNow("v0.1.0", &out, &errOut)
	if err == nil {
		t.Fatal("expected network error to be returned, got nil")
	}
}

// mustCachePath returns the resolved cache-file path or fails the test.
func mustCachePath(t *testing.T) string {
	t.Helper()
	p, err := cachePath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Compile-time check that fmt is reachable (avoids unused-import
// warnings when the file is edited).
var _ = fmt.Sprintf
