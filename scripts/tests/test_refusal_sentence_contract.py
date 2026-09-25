"""Every refusal sentence the backend can return must be recognised by the evaluator.

The failure this pins has already happened once. `scripts/run-evals.py` decides
whether a negative case passed by looking for a refusal marker in the answer
text; when the backend started returning a reason-specific sentence instead of
the single generic one, the marker table was updated by hand. Nothing enforced
that, so the next reason would have shipped a *correct* refusal that the
evaluator scored as a hallucinated answer — the suite would have gone red for
the wrong reason, and the fix would have looked like "loosen the assertion".

The two lists live in different languages, so this test reads both as text:

  services/etl-worker/internal/query/service.go  -> refusalAnswers (reason -> sentence)
  scripts/run-evals.py                           -> NEGATIVE_FALLBACK_MARKERS

Contract: every sentence in refusalAnswers contains at least one marker.
"""

import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SERVICE_GO = ROOT / "services/etl-worker/internal/query/service.go"
RUN_EVALS = ROOT / "scripts/run-evals.py"

# Guard against a regex that silently matches nothing: the backend has had at
# least this many reasons since reason-specific sentences were introduced.
MIN_REASONS = 6

_QUOTED = re.compile(r'"([^"]*)"')
_IDENTIFIER = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")


def _block(text, opener):
    """Return the text between `opener` and its matching closing brace/paren."""
    start = text.index(opener)
    # The opener is a name, not a delimiter: find the first `{` or `(` after it.
    candidates = [i for i in (text.find("{", start), text.find("(", start)) if i != -1]
    if not candidates:
        raise AssertionError("no block delimiter after %r" % opener)
    start = min(candidates)
    depth = 0
    for index in range(start, len(text)):
        char = text[index]
        if char in "{(":
            depth += 1
        elif char in "})":
            depth -= 1
            if depth == 0:
                return text[start + 1 : index]
    raise AssertionError("unterminated block for %r" % opener)


def _string_constants(go_source):
    """Map a Go string constant name to its literal, for the refusal sentences."""
    constants = {}
    for match in re.finditer(r"^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*\"([^\"]*)\"\s*$", go_source, re.M):
        constants[match.group(1)] = match.group(2)
    return constants


def _refusal_sentences(go_source):
    """Every sentence `refusalAnswer` can return, in declaration order."""
    constants = _string_constants(go_source)
    sentences = []
    for line in _block(go_source, "var refusalAnswers").splitlines():
        line = line.strip().rstrip(",")
        if not line or ":" not in line:
            continue
        value = line.split(":", 1)[1].strip()
        literals = _QUOTED.findall(value)
        if literals:
            sentences.extend(literals)
            continue
        # A named constant (e.g. SensitiveAnswerRefusal) carries the sentence.
        if _IDENTIFIER.match(value):
            if value not in constants:
                raise AssertionError("cannot resolve refusal sentence constant %r" % value)
            sentences.append(constants[value])
    return sentences


def _negative_markers(source):
    return _QUOTED.findall(_block(source, "NEGATIVE_FALLBACK_MARKERS"))


class RefusalSentenceContractTests(unittest.TestCase):
    def setUp(self):
        self.sentences = _refusal_sentences(SERVICE_GO.read_text(encoding="utf-8"))
        self.markers = _negative_markers(RUN_EVALS.read_text(encoding="utf-8"))

    def test_the_parse_actually_found_the_reasons(self):
        self.assertGreaterEqual(
            len(self.sentences),
            MIN_REASONS,
            "parsed only %d refusal sentences; the source shape changed and this "
            "test stopped checking anything" % len(self.sentences),
        )
        self.assertTrue(self.markers, "parsed no negative fallback markers")

    def test_every_refusal_sentence_is_recognised_by_the_evaluator(self):
        unrecognised = [
            sentence
            for sentence in self.sentences
            if not any(marker in sentence for marker in self.markers)
        ]
        self.assertEqual(
            [],
            unrecognised,
            "these backend refusal sentences have no entry in "
            "scripts/run-evals.py NEGATIVE_FALLBACK_MARKERS, so the evaluator "
            "would score a correct refusal as a hallucinated answer: %r" % unrecognised,
        )

    def test_no_sentence_is_the_generic_fallback(self):
        """A reason that falls back to the generic sentence tells the caller nothing."""
        generic = "未找到相关文档，无法回答该问题。"
        self.assertNotIn(
            generic,
            self.sentences,
            "a refusal reason is using the generic sentence instead of a specific one",
        )


if __name__ == "__main__":
    unittest.main()
