#!/usr/bin/env python3
"""Extract or inject the intro paragraph of a single-version changie file.

A version file looks like:

    ## [0.10.0] - 2026-06-21

    <intro paragraph>

    ### Added

    - ...

The intro is the text between the `## [` header line and the first
`### ` kind header. Used by release-pr.yaml to preserve a hand-edited
intro across bot re-runs and to inject a freshly drafted one.
"""
import sys


def _split(lines):
    """Return (header_idx, kind_idx): the `## [` line index and the index
    of the first `### ` line (len(lines) if there is none)."""
    header_idx = None
    for i, line in enumerate(lines):
        if line.startswith("## ["):
            header_idx = i
            break
    if header_idx is None:
        raise ValueError("no version header (`## [`) found")
    kind_idx = len(lines)
    for i in range(header_idx + 1, len(lines)):
        if lines[i].startswith("### "):
            kind_idx = i
            break
    return header_idx, kind_idx


def extract(text):
    lines = text.splitlines()
    header_idx, kind_idx = _split(lines)
    return "\n".join(lines[header_idx + 1:kind_idx]).strip()


def inject(text, intro):
    lines = text.splitlines()
    header_idx, kind_idx = _split(lines)
    header = lines[header_idx]
    rest = lines[kind_idx:]
    intro = intro.strip()
    block = [header, "", intro, ""] if intro else [header, ""]
    return "\n".join(block + rest).rstrip("\n") + "\n"


def main(argv):
    if len(argv) < 3:
        sys.exit("usage: changelog_intro.py {extract|inject} <version-file> [intro-file]")
    cmd, version_file = argv[1], argv[2]
    with open(version_file) as f:
        text = f.read()
    if cmd == "extract":
        print(extract(text))
    elif cmd == "inject":
        if len(argv) < 4:
            sys.exit("inject requires <intro-file>")
        with open(argv[3]) as f:
            intro = f.read()
        with open(version_file, "w") as f:
            f.write(inject(text, intro))
    else:
        sys.exit(f"unknown command: {cmd}")


if __name__ == "__main__":
    main(sys.argv)
