import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import changelog_intro as ci

NO_INTRO = "## [0.10.0] - 2026-06-21\n\n### Added\n\n- A feature.\n"
WITH_INTRO = "## [0.10.0] - 2026-06-21\n\nA short intro paragraph.\n\n### Added\n\n- A feature.\n"


class ExtractTests(unittest.TestCase):
    def test_no_intro_returns_empty(self):
        self.assertEqual(ci.extract(NO_INTRO), "")

    def test_with_intro_returns_text(self):
        self.assertEqual(ci.extract(WITH_INTRO), "A short intro paragraph.")

    def test_missing_header_raises(self):
        with self.assertRaises(ValueError):
            ci.extract("no version header here\n")


class InjectTests(unittest.TestCase):
    def test_inject_into_no_intro(self):
        self.assertEqual(ci.inject(NO_INTRO, "A short intro paragraph."), WITH_INTRO)

    def test_inject_replaces_existing_intro(self):
        replaced = ci.inject(WITH_INTRO, "Brand new intro.")
        self.assertEqual(ci.extract(replaced), "Brand new intro.")
        self.assertEqual(replaced.count("###"), 1)

    def test_inject_empty_intro_is_noop_shape(self):
        self.assertEqual(ci.inject(NO_INTRO, ""), NO_INTRO)

    def test_roundtrip_preserves_intro(self):
        self.assertEqual(ci.inject(WITH_INTRO, ci.extract(WITH_INTRO)), WITH_INTRO)


if __name__ == "__main__":
    unittest.main()
