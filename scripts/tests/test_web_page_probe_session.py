"""Contract tests for the session handling in scripts/web-page-probe.cjs.

The probe drives a persistent Chrome profile. When that profile already holds a
session, /login hands an authenticated visitor straight to the workspace, so the
demo button never enters the DOM and `--login` used to abort with
"demo login button not found on /login (missing)" -- which made the probe fail
on its own second run against the same profile.

The fix reuses the session, but only on evidence, and the two predicates that
decide it are exercised by running them (`--self-check`) rather than by reading
the source. The source assertions below are wiring tripwires on top: a correct
predicate that is not consulted, or a bounce that is detected but not folded
into the exit code, is the same failure one level up.
"""

import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PROBE = ROOT / "scripts" / "web-page-probe.cjs"


class WebPageProbeSessionTests(unittest.TestCase):
    def test_session_predicates_hold_when_executed(self):
        proc = subprocess.run(
            ["node", str(PROBE), "--self-check"],
            cwd=ROOT,
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("predicate(s) ok", proc.stdout)

    def test_login_skips_only_on_a_redirect(self):
        source = PROBE.read_text(encoding="utf-8")
        # The reuse branch must be gated on the redirect predicate, not on
        # "the button was not there".
        self.assertIn("} else if (isLoginRedirect(loginRedirectTo, opts.base)) {", source)
        self.assertIn('loginOutcome = "already-authenticated";', source)
        self.assertIn("demo login button not found on /login", source)

    def test_a_route_that_lands_on_login_fails_the_probe(self):
        source = PROBE.read_text(encoding="utf-8")
        self.assertIn("function bouncedToLogin(route, href)", source)
        self.assertIn("const bounced = bouncedToLogin(route, value.href || url);", source)
        self.assertIn("item.bouncedToLogin,", source)
        self.assertIn("bounced to /login", source)


if __name__ == "__main__":
    unittest.main()
