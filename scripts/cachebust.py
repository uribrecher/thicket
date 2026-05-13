#!/usr/bin/env python3
"""Stamp docs/index.html with the current SHA-256 short-hash of
docs/styles.css so the browser cache is invalidated whenever the
stylesheet changes.

Idempotent: rewriting with the same hash is a no-op. Called from
`task pages:cachebust` and from the cachebust-check CI workflow.
"""

import hashlib
import pathlib
import re
import sys

CSS_PATH = pathlib.Path("docs/styles.css")
HTML_PATH = pathlib.Path("docs/index.html")
STAMP_RE = re.compile(r"styles\.css\?v=[a-f0-9]{8}")


def main() -> int:
    if not CSS_PATH.exists() or not HTML_PATH.exists():
        print(f"missing required file under {CSS_PATH.parent}/", file=sys.stderr)
        return 2
    digest = hashlib.sha256(CSS_PATH.read_bytes()).hexdigest()[:8]
    html = HTML_PATH.read_text()
    new_html, n = STAMP_RE.subn(f"styles.css?v={digest}", html)
    # The whole point of this script is to keep the `?v=...` stamp
    # in sync with styles.css. If the regex matches zero times,
    # someone removed or reshaped the stamp — silently succeeding
    # would let the CI check pass while the cache-busting invariant
    # is gone. Fail loudly so the human is forced to investigate.
    if n == 0:
        print(
            f"error: no `styles.css?v=<8 hex chars>` stamp found in {HTML_PATH}. "
            "Restore the `?v=...` query string on the `<link rel=\"stylesheet\">` "
            "so future CSS edits remain cache-bustable.",
            file=sys.stderr,
        )
        return 3
    if n > 1:
        print(
            f"error: expected exactly one `styles.css?v=...` stamp in {HTML_PATH}, "
            f"found {n}. The cache-busting workflow only knows how to manage one.",
            file=sys.stderr,
        )
        return 4
    if new_html != html:
        HTML_PATH.write_text(new_html)
    print(f"docs/index.html → styles.css?v={digest}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
