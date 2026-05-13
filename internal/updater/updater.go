// Package updater implements the `thicket` self-update check and the
// in-Go install path. The check runs on every command (gated by a 24h
// cache and the THICKET_NO_UPDATE_CHECK env var) and, when a newer
// release exists, prompts the user. On confirm we download the right
// release tarball, verify SHA-256 against the release's checksums.txt,
// and atomically swap the running binary in place.
//
// Two callers exist:
//
//   - rootCmd's PersistentPreRunE → CheckOnRun (24h cache, soft fail)
//   - `thicket update` subcommand → CheckAndApplyNow (no cache, hard
//     fail if anything misbehaves)
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

// httpClient is a package-global so tests can swap it for an httptest
// server. Timeout is short so a flaky network never delays the user's
// actual command.
var httpClient = &http.Client{Timeout: 4 * time.Second}

// CheckOnRun is the lightweight "did a new release land?" probe. It
// is safe to call before every command:
//
//   - silent on success of "no update needed"
//   - silent on any network/parse error (we never block the user's
//     actual work)
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

	cache, _ := loadCache()
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
		// 24h window — don't pester them again until the cache
		// expires or a newer release lands.
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
func promptAndApply(currentVersion, latestTag string, out, errOut io.Writer) {
	if !isTerminal(errOut) {
		// Non-interactive shell / CI / piped output — never prompt;
		// instead print a one-line hint so the user knows.
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
	if err != nil || !confirmed {
		// Remember the decline so we don't re-ask for the same
		// version within the 24h cache window.
		st, _ := loadCache()
		st.DeclinedVersion = latestTag
		_ = saveCache(st)
		fmt.Fprintln(errOut, "Skipping update. Set THICKET_NO_UPDATE_CHECK=1 to silence this prompt entirely.")
		return
	}

	fmt.Fprintf(errOut, "Updating thicket %s → %s …\n", currentVersion, latestTag)
	if err := Apply(latestTag); err != nil {
		if err == ErrUnmanagedInstall {
			fmt.Fprintln(errOut, "This thicket binary lives outside a managed install path, so")
			fmt.Fprintln(errOut, "auto-update won't touch it. To update manually, run:")
			fmt.Fprintln(errOut, "  curl -fsSL https://github.com/uribrecher/thicket/releases/latest/download/install.sh | sh")
			fmt.Fprintln(errOut, "or, for go-install users: `go install github.com/uribrecher/thicket/cmd/thicket@latest`.")
			return
		}
		fmt.Fprintf(errOut, "update failed: %v\n", err)
		return
	}
	fmt.Fprintf(errOut, "thicket updated to %s. Continuing with your original command using the previous binary…\n", latestTag)
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
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repoSlug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "thicket-self-update")
	resp, err := httpClient.Do(req)
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
