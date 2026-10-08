import argparse
import hashlib
import html
import json
import re
import subprocess
import sys
from collections import Counter
from pathlib import Path
from urllib.parse import unquote


def normalize(text):
    for _ in range(3):
        decoded = html.unescape(unquote(text))
        decoded = re.sub(
            r"\\(?:u([0-9a-fA-F]{4})|x([0-9a-fA-F]{2}))",
            lambda match: chr(int(match[1] or match[2], 16)),
            decoded,
        )
        if decoded == text:
            break
        text = decoded
    return text


def matching_identifier_fingerprints(text, fingerprints):
    text = normalize(text)
    candidates = re.findall(r"[A-Za-z0-9]+", text)
    for domain in re.findall(
        r"(?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,63}(?![A-Za-z0-9_-])", text
    ):
        labels = domain.lower().split(".")
        candidates.extend(
            ".".join(labels[index:]) for index in range(len(labels) - 1)
        )
    return [
        digest
        for candidate in candidates
        if (digest := hashlib.sha256(candidate.encode()).hexdigest()) in fingerprints
    ]


def contains_identifier(text, fingerprints):
    return bool(matching_identifier_fingerprints(text, fingerprints))


def git(*args):
    return subprocess.run(
        ["git", "--literal-pathspecs", *args],
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    ).stdout


def contains_identifier_in_path(path, fingerprints):
    for component in normalize(path).split("/"):
        while component:
            if contains_identifier(component, fingerprints):
                return True
            if component.lower().endswith(".invalid"):
                break
            if "." not in component:
                break
            component = component.rsplit(".", 1)[0]
    return False


def added_lines(patch):
    line_number = None
    for line in patch.splitlines():
        hunk = re.match(r"@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@", line)
        if hunk:
            line_number = int(hunk[1])
        elif line_number is not None and line.startswith("+"):
            yield line_number, line[1:]
            line_number += 1
        elif line_number is not None and line.startswith(" "):
            line_number += 1


def removed_lines(patch):
    in_hunk = False
    for line in patch.splitlines():
        if line.startswith("@@ "):
            in_hunk = True
        elif in_hunk and line.startswith("-"):
            yield line[1:]


def findings(base, head, fingerprints, cached=False):
    revisions = [base, head] if head else [base]
    options = ["--no-ext-diff", "--no-textconv", "--no-renames", "--no-color"]
    if cached:
        options.append("--cached")
    paths = git(
        "diff", *options, "--name-only", "-z", "--diff-filter=ACMT", *revisions, "--"
    )
    introduced_paths = set(
        git(
            "diff", *options, "--name-only", "-z", "--diff-filter=AC", *revisions, "--"
        ).split(b"\0")
    )
    for raw_path in paths.split(b"\0"):
        if not raw_path:
            continue
        path = raw_path.decode("utf-8", errors="surrogateescape")
        restricted_path = contains_identifier_in_path(path, fingerprints)
        if restricted_path and raw_path in introduced_paths:
            yield "<redacted filename>", 0
        patch = git("diff", *options, "--text", "--unified=0", *revisions, "--", path)
        decoded_patch = patch.decode("utf-8", errors="replace")
        existing_identifiers = Counter(
            fingerprint
            for line in removed_lines(decoded_patch)
            for fingerprint in matching_identifier_fingerprints(line, fingerprints)
        )
        for number, line in added_lines(decoded_patch):
            added_identifiers = Counter(
                matching_identifier_fingerprints(line, fingerprints)
            )
            newly_added = False
            for fingerprint, count in added_identifiers.items():
                reused = min(count, existing_identifiers[fingerprint])
                existing_identifiers[fingerprint] -= reused
                if count > reused:
                    newly_added = True
            if newly_added:
                display_path = "<redacted filename>" if restricted_path else ascii(path)
                yield display_path, number


def main():
    parser = argparse.ArgumentParser(
        description="Reject known tenant identifiers in added lines."
    )
    parser.add_argument("--base", required=True, help="Base branch or commit")
    parser.add_argument(
        "--head", help="Head commit; omit for staged and tracked working-tree changes"
    )
    args = parser.parse_args()
    try:
        fingerprints = set(
            json.loads(
                Path(__file__).with_name("restricted-fingerprints.json").read_text()
            )
        )
        if not fingerprints or any(
            not re.fullmatch(r"[0-9a-f]{64}", value) for value in fingerprints
        ):
            raise ValueError("Invalid fingerprint configuration")
        base = git("rev-parse", "--verify", f"{args.base}^{{commit}}").decode().strip()
        head = (
            git("rev-parse", "--verify", f"{args.head or 'HEAD'}^{{commit}}")
            .decode()
            .strip()
        )
        ancestor = git("merge-base", base, head).decode().strip()
        matches = list(findings(ancestor, head if args.head else None, fingerprints))
        if not args.head:
            matches.extend(findings(ancestor, None, fingerprints, cached=True))
        matches = list(dict.fromkeys(matches))
    except (OSError, subprocess.CalledProcessError, ValueError, TypeError):
        print(
            "Tenant identifier check could not complete. "
            "Check Git refs and fingerprint configuration.",
            file=sys.stderr,
        )
        return 2
    for path, number in matches:
        print(
            f"{path}:{number}: restricted tenant identifier; "
            "use synthetic data or existing runtime configuration."
        )
    if matches:
        print("Do not copy matched values into logs, review comments, or new fixtures.")
        return 1
    print("No known tenant identifiers found in added lines or filenames.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
