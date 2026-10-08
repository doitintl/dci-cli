import hashlib
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest.mock import patch

import check_identifiers as guard

SYNTHETIC_ID = "SyntheticCustomerABC123"
SYNTHETIC_DOMAIN = "tenant.example.com"
FINGERPRINTS = {
    hashlib.sha256(value.encode()).hexdigest()
    for value in [SYNTHETIC_ID, SYNTHETIC_DOMAIN]
}


class IdentifierTests(unittest.TestCase):
    def test_detects_identifiers_in_text_and_urls(self):
        for value in [
            f'"customerId": "{SYNTHETIC_ID}"',
            f"https://console.example.com/customers/{SYNTHETIC_ID}/reports",
            f"prefix_{SYNTHETIC_ID}_suffix",
            f"user@{SYNTHETIC_DOMAIN.upper()}",
            f"https://sub.{SYNTHETIC_DOMAIN}/",
        ]:
            with self.subTest(value=value):
                self.assertTrue(guard.contains_identifier(value, FINGERPRINTS))

    def test_detects_escaped_identifiers(self):
        for value in [
            "".join(f"%{ord(char):02x}" for char in SYNTHETIC_ID),
            "".join(f"&#{ord(char)};" for char in SYNTHETIC_ID),
            "".join(f"\\u{ord(char):04x}" for char in SYNTHETIC_ID),
            "".join(f"\\x{ord(char):02x}" for char in SYNTHETIC_ID),
            SYNTHETIC_DOMAIN.replace(".", "%252e"),
        ]:
            with self.subTest(value=value):
                self.assertTrue(guard.contains_identifier(value, FINGERPRINTS))

    def test_allows_placeholders_other_values_and_product_endpoints(self):
        for value in [
            "{CUSTOMER_ID}",
            "test-customer-a",
            "https://api.doit.com",
            "https://console.example.com",
            SYNTHETIC_ID.lower(),
            SYNTHETIC_DOMAIN + ".invalid",
            SYNTHETIC_DOMAIN + "1",
            SYNTHETIC_DOMAIN + "-foo",
            SYNTHETIC_DOMAIN + "_suffix",
        ]:
            with self.subTest(value=value):
                self.assertFalse(guard.contains_identifier(value, FINGERPRINTS))

    def test_hunk_line_numbers_and_header_like_content(self):
        diff = "+++ b/docs.md\n@@ -2 +2,2 @@\n-old\n+++content\n+new\n@@ -8,0 +10 @@\n+last\n"
        self.assertEqual(
            list(guard.added_lines(diff)), [(2, "++content"), (3, "new"), (10, "last")]
        )


class GitDiffTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.cwd = Path.cwd()
        os.chdir(self.directory.name)
        self.addCleanup(os.chdir, self.cwd)
        self.run_git("init", "-q")
        self.run_git("config", "user.email", "test@example.com")
        self.run_git("config", "user.name", "Test")
        self.write("runtime.txt", SYNTHETIC_ID + "\nold\n")
        self.run_git("add", ".")
        self.commit()
        self.base = self.run_git("rev-parse", "HEAD").strip()

    def run_git(self, *args):
        return subprocess.run(
            ["git", "-c", "core.hooksPath=/dev/null", *args],
            check=True,
            capture_output=True,
            text=True,
        ).stdout

    def write(self, path, text):
        destination = Path(path)
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_text(text)

    def commit(self):
        self.run_git("-c", "commit.gpgsign=false", "commit", "-qm", "test fixture")

    def scan(self, head=None):
        return list(guard.findings(self.base, head, FINGERPRINTS))

    def test_preserves_unchanged_runtime_and_allows_removal(self):
        self.write("runtime.txt", SYNTHETIC_ID + "\nnew\n")
        self.assertEqual(self.scan(), [])
        self.write("runtime.txt", "new\n")
        self.assertEqual(self.scan(), [])

    def test_allows_count_preserving_runtime_format_and_comment_changes(self):
        self.write("runtime.txt", f"  {SYNTHETIC_ID}  # required routing\nold\n")
        self.assertEqual(self.scan(), [])

    def test_allows_runtime_reorder_and_line_ending_changes(self):
        self.write("runtime.txt", f"old\n{SYNTHETIC_ID}\n")
        self.assertEqual(self.scan(), [])
        Path("runtime.txt").write_bytes(f"{SYNTHETIC_ID}\r\nold\r\n".encode())
        self.assertEqual(self.scan(), [])

    def test_rejects_increased_or_cross_file_identifier_occurrences(self):
        self.write("runtime.txt", f"{SYNTHETIC_ID}\nold\n{SYNTHETIC_ID}\n")
        self.assertEqual(self.scan(), [("'runtime.txt'", 3)])
        self.write("runtime.txt", "old\n")
        self.write("public.md", SYNTHETIC_ID + "\n")
        self.run_git("add", "public.md")
        self.assertEqual(self.scan(), [("'public.md'", 1)])

    def test_scans_staged_new_files_across_surfaces(self):
        for path in [
            ".hidden/example.md",
            "test [fixture].json",
            "docs/guide.mdx",
            "saved.ipynb",
        ]:
            self.write(path, SYNTHETIC_ID + "\n")
            self.run_git("add", "--", path)
        self.assertEqual(len(self.scan()), 4)

    def test_renamed_file_is_checked_at_new_destination(self):
        self.run_git("mv", "runtime.txt", "public.md")
        self.assertEqual(self.scan(), [("'public.md'", 1)])

    def test_scans_committed_head_and_ignores_later_worktree_changes(self):
        self.write("guide.md", SYNTHETIC_ID + "\n")
        self.run_git("add", ".")
        self.commit()
        self.write("guide.md", "{CUSTOMER_ID}\n")
        self.assertEqual(self.scan("HEAD"), [("'guide.md'", 1)])
        self.assertEqual(self.scan(), [])

    def test_filename_is_redacted(self):
        path = f"docs/{SYNTHETIC_ID}.md"
        self.write(path, SYNTHETIC_ID + "\n")
        self.run_git("add", ".")
        self.assertEqual(
            self.scan(), [("<redacted filename>", 0), ("<redacted filename>", 1)]
        )

    def test_domain_filename_with_multiple_extensions_is_redacted(self):
        self.write(f"docs/{SYNTHETIC_DOMAIN}.md.backup", SYNTHETIC_ID + "\n")
        self.run_git("add", ".")
        self.assertEqual(
            self.scan(), [("<redacted filename>", 0), ("<redacted filename>", 1)]
        )

    def test_domain_filename_is_detected_without_a_content_match(self):
        self.write(f"docs/{SYNTHETIC_DOMAIN}.md", "{CUSTOMER_ID}\n")
        self.run_git("add", ".")
        self.assertEqual(self.scan(), [("<redacted filename>", 0)])

    def test_allows_invalid_suffixed_domain_filename(self):
        path = f"docs/{SYNTHETIC_DOMAIN}.invalid.md"
        self.write(path, "{CUSTOMER_ID}\n")
        self.run_git("add", ".")
        self.assertEqual(self.scan(), [])

    def test_allows_cleanup_of_existing_restricted_filename(self):
        path = f"docs/{SYNTHETIC_ID}.md"
        self.write(path, SYNTHETIC_ID + "\n")
        self.run_git("add", ".")
        self.commit()
        self.base = self.run_git("rev-parse", "HEAD").strip()
        self.write(path, "{CUSTOMER_ID}\n")
        self.assertEqual(self.scan(), [])
        self.run_git("update-index", "--chmod=+x", "--", path)
        self.assertEqual(self.scan(), [])

    def test_existing_restricted_filename_still_redacts_new_content_match(self):
        path = f"docs/{SYNTHETIC_ID}.md"
        self.write(path, "{CUSTOMER_ID}\n")
        self.run_git("add", ".")
        self.commit()
        self.base = self.run_git("rev-parse", "HEAD").strip()
        self.write(path, SYNTHETIC_ID + "\n")
        self.assertEqual(self.scan(), [("<redacted filename>", 1)])

    def test_forced_git_color_cannot_hide_matches(self):
        self.run_git("config", "color.ui", "always")
        self.write("guide.md", SYNTHETIC_ID + "\n")
        self.run_git("add", ".")
        self.assertEqual(self.scan(), [("'guide.md'", 1)])

    def test_cli_catches_staged_identifiers_hidden_by_unstaged_edits(self):
        self.write("guide.md", SYNTHETIC_ID + "\n")
        self.run_git("add", ".")
        self.write("guide.md", "{CUSTOMER_ID}\n")
        output = io.StringIO()
        with patch.object(sys, "argv", ["check", "--base", self.base]):
            with patch.object(
                Path, "read_text", return_value=json.dumps(sorted(FINGERPRINTS))
            ):
                with redirect_stdout(output):
                    self.assertEqual(guard.main(), 1)
        self.assertIn("guide.md", output.getvalue())
        self.assertNotIn(SYNTHETIC_ID, output.getvalue())

    def test_cli_fails_without_echoing_matches_or_git_errors(self):
        script = Path("check.py")
        shutil.copyfile(Path(guard.__file__), script)
        Path("restricted-fingerprints.json").write_text(
            json.dumps(sorted(FINGERPRINTS))
        )
        self.write("guide.md", SYNTHETIC_ID + "\n")
        self.run_git("add", "guide.md")
        result = subprocess.run(
            [sys.executable, str(script), "--base", self.base],
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("guide.md", result.stdout)
        self.assertNotIn(SYNTHETIC_ID, result.stdout + result.stderr)
        result = subprocess.run(
            [sys.executable, str(script), "--base", SYNTHETIC_ID],
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 2)
        self.assertNotIn(SYNTHETIC_ID, result.stdout + result.stderr)

    def test_cli_rejects_missing_or_empty_fingerprints(self):
        with patch.object(sys, "argv", ["check", "--base", self.base]):
            with patch.object(Path, "read_text", side_effect=OSError):
                self.assertEqual(guard.main(), 2)
            with patch.object(Path, "read_text", return_value="[]"):
                self.assertEqual(guard.main(), 2)


if __name__ == "__main__":
    unittest.main()
