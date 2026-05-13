// Package updater implements the `thicket` self-update check and the
// in-Go install path. The check runs on most commands (excluding
// `version`, `help`, and the manual `update` subcommand itself),
// gated by a 24h cache and the THICKET_NO_UPDATE_CHECK env var. When
// a newer release exists we either prompt the user (TTY + managed
// install), print a one-line install hint (TTY + unmanaged install
// or non-TTY), or stay silent (cache hit on a known-declined
// version). On confirm we download the right release tarball, verify
// SHA-256 against the release's checksums.txt, and atomically swap
// the running binary in place.
//
// Two entry points:
//
//   - rootCmd's PersistentPreRun → CheckOnRun (24h cache, soft fail)
//   - `thicket update` subcommand → CheckAndApplyNow (no cache, hard
//     fail if anything misbehaves)
package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"
)

// repoSlug is the GitHub owner/name pair used to build API + release
// URLs. Lives in one place so it's easy to fork.
const repoSlug = "uribrecher/thicket"

// EnvDisable, when set to a truthy value, suppresses the background
// update check (the manual `thicket update` command still works).
const EnvDisable = "THICKET_NO_UPDATE_CHECK"

// probeClient is used for the cheap "what's the latest tag?" call:
// short-timeout so the pre-run probe can't stall a user's command
// for more than a couple seconds on a flaky network. Cache hits skip
// the HTTP entirely, so the typical user pays nothing.
//
// downloadClient covers the actual binary + checksums.txt downloads
// during Apply(). Those are multi-MB and may follow GitHub redirects,
// so they get a much longer timeout. Keeping the two clients separate
// means we can't accidentally regress probe latency by tuning the
// download path (or vice versa).
//
// Both are package-global so tests can swap them for an httptest
// server.
var (
	probeClient    = &http.Client{Timeout: 2 * time.Second}
	downloadClient = &http.Client{Timeout: 60 * time.Second}
)

// apiBaseURL is the GitHub API root used to look up `releases/latest`.
// Test-only override.
var apiBaseURL = "https://api.github.com"

// CheckOnRun is the lightweight "did a new release land?" probe.
// Synchronous on the calling goroutine: cache hits are effectively
// free; cache misses pay one HTTP round-trip bounded by the package
// httpClient timeout (currently 2s). Safe to call before every
// command:
//
//   - silent on success of "no update needed"
//   - silent on any network/parse error (we never block the user's
//     actual work for longer than the HTTP timeout)
//   - caches the last-checked-at + latest-version in xdg for 24h
//   - skips if THICKET_NO_UPDATE_CHECK is set, if currentVersion is
//     a dev/dirty build, or if stderr is not a TTY (no point
//     prompting nobody — instead print a one-line hint)
//
// When the check turns up a newer version, the user is prompted
// (interactive) or hinted (non-interactive). On confirm we call
// Apply().
func CheckOnRun(currentVersion string, out, errOut io.Writer) {
	if os.Getenv(EnvDisable) != "" && os.Getenv(EnvDisable) != "0" {
		return
	}
	cur, err := ParseRelease(currentVersion)
	if err != nil {
		// Dev / dirty / unparseable build — skip silently.
		return
	}

	cache, err := loadCache()
	if err != nil {
		// Cache I/O failure (permission denied, broken fs, etc.).
		// Soft-fail the whole probe so we don't end up doing an
		// uncached GitHub fetch on every single command.
		return
	}
	var latestTag string
	if cache.fresh() {
		latestTag = cache.LatestVersion
	} else {
		tag, err := fetchLatestTag(context.Background())
		if err != nil {
			// Soft fail — no update check is better than blocking.
			return
		}
		latestTag = tag
		_ = saveCache(cacheState{
			CheckedAt:       time.Now(),
			LatestVersion:   tag,
			DeclinedVersion: cache.DeclinedVersion,
		})
	}

	latest, err := ParseRelease(latestTag)
	if err != nil {
		return
	}
	if cur.Compare(latest) >= 0 {
		return
	}
	if cache.DeclinedVersion == latestTag {
		// User said "no" to this exact version inside the current
		// 24h window, OR we already printed the unmanaged-install
		// install hint once for this version — don't pester them
		// again until the cache expires or a newer release lands.
		return
	}

	promptAndApply(currentVersion, latestTag, out, errOut)
}

// CheckAndApplyNow is `thicket update`: force-refresh, don't gate on
// the 24h cache, and surface errors loudly. Returns nil and prints
// "already on the latest" when the local version is current.
func CheckAndApplyNow(currentVersion string, out, errOut io.Writer) error {
	cur, err := ParseRelease(currentVersion)
	if err != nil {
		return fmt.Errorf("current version %q is not a released build; auto-update is only supported for tagged releases", currentVersion)
	}
	tag, err := fetchLatestTag(context.Background())
	if err != nil {
		return fmt.Errorf("fetch latest release: %w", err)
	}
	_ = saveCache(cacheState{CheckedAt: time.Now(), LatestVersion: tag})

	latest, err := ParseRelease(tag)
	if err != nil {
		return fmt.Errorf("parse latest tag %q: %w", tag, err)
	}
	if cur.Compare(latest) >= 0 {
		fmt.Fprintf(out, "thicket is already at the latest release (%s).\n", currentVersion)
		return nil
	}
	fmt.Fprintf(out, "Updating thicket %s → %s …\n", currentVersion, tag)
	if err := Apply(tag); err != nil {
		return err
	}
	fmt.Fprintf(out, "thicket updated to %s\n", tag)
	return nil
}

// promptAndApply renders the interactive (or non-interactive) gate
// in front of Apply(). Used by CheckOnRun only.
//
// Three layers, top to bottom:
//
//  1. Unmanaged install (brew / nix / go install / source build) —
//     auto-update can't touch the binary, so print a one-line install
//     hint and persist the decline (so the same version doesn't
//     re-print every command for 24h).
//  2. Non-TTY (CI / pipes) — managed install but can't prompt; print
//     a hint and leave it at that.
//  3. TTY + managed — render the huh confirm; on yes, Apply().
func promptAndApply(currentVersion, latestTag string, out, errOut io.Writer) {
	_, managed := resolveAndCheckManaged()
	if !managed {
		printUnmanagedHint(errOut, currentVersion, latestTag)
		if st, err := loadCache(); err == nil {
			st.DeclinedVersion = latestTag
			_ = saveCache(st)
		}
		return
	}
	if !isTerminal(errOut) {
		fmt.Fprintf(errOut, "thicket: a newer release is available (%s → %s). Run `thicket update` to apply.\n",
			currentVersion, latestTag)
		return
	}

	fmt.Fprintf(errOut, "\nA newer thicket release is available: %s → %s\n",
		currentVersion, latestTag)

	confirmed := false
	err := huh.NewConfirm().
		Title("Update now?").
		Description("Replaces the current `thicket` binary in place.").
		Affirmative("Yes, update").
		Negative("Not now").
		Value(&confirmed).
		Run()
	if err != nil {
		// Prompt itself failed (terminal init issue, Ctrl-C, etc.).
		// Treat as a soft-fail: don't persist a decline, so we'll try
		// again next invocation. This avoids silencing the prompt for
		// 24h after a transient TTY hiccup.
		return
	}
	if !confirmed {
		// Explicit "no". Remember the decline so we don't re-ask
		// for the same version within the 24h cache window.
		st, _ := loadCache()
		st.DeclinedVersion = latestTag
		_ = saveCache(st)
		fmt.Fprintln(errOut, "Skipping update. Set THICKET_NO_UPDATE_CHECK=1 to silence this prompt entirely.")
		return
	}

	fmt.Fprintf(errOut, "Updating thicket %s → %s …\n", currentVersion, latestTag)
	if err := Apply(latestTag); err != nil {
		// Eligibility was already checked above, so ErrUnmanagedInstall
		// shouldn't fire here in normal flow. Handle it defensively in
		// case the binary moved between resolveAndCheckManaged() and
		// Apply() (highly unlikely but possible).
		if errors.Is(err, ErrUnmanagedInstall) {
			printUnmanagedHint(errOut, currentVersion, latestTag)
			return
		}
		fmt.Fprintf(errOut, "update failed: %v\n", err)
		return
	}
	fmt.Fprintf(errOut, "thicket updated to %s. Continuing with your original command using the previous binary…\n", latestTag)
}

// resolveAndCheckManaged returns the symlink-resolved current
// executable path and whether it lives in a managed-install path
// (auto-update can safely write to it). On any resolution error we
// return ("", false), which routes the caller into the unmanaged-
// install hint path — safer than guessing.
func resolveAndCheckManaged() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", false
	}
	return resolved, IsManagedInstall(resolved)
}

func printUnmanagedHint(w io.Writer, currentVersion, latestTag string) {
	fmt.Fprintf(w, "\nA newer thicket release is available: %s → %s\n", currentVersion, latestTag)
	fmt.Fprintln(w, "(this binary lives outside a managed install path, so auto-update won't touch it)")
	fmt.Fprintln(w, "To update:")
	fmt.Fprintln(w, "  curl -fsSL https://github.com/uribrecher/thicket/releases/latest/download/install.sh | sh")
	fmt.Fprintln(w, "or, for go-install users: `go install github.com/uribrecher/thicket/cmd/thicket@latest`.")
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// fetchLatestTag hits GitHub's releases/latest endpoint and returns
// the tag_name (e.g. "v0.1.2"). No auth — public repo.
func fetchLatestTag(ctx context.Context) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", apiBaseURL, repoSlug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "thicket-self-update")
	resp, err := probeClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.TagName == "" {
		return "", fmt.Errorf("empty tag_name in response")
	}
	return body.TagName, nil
}
