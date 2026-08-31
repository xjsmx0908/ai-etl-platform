import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


class ReauthenticationWebContractTests(unittest.TestCase):
    def test_start_requires_explicit_same_origin_and_scoped_state_cookie(self):
        source = (ROOT / "web/app/api/auth/reauth/start/route.ts").read_text(encoding="utf-8")
        self.assertIn('originURL.host === host', source)
        self.assertIn('originURL.protocol === expectedProtocol', source)
        self.assertIn('fetchSite !== "same-origin"', source)
        self.assertIn('"ai_etl_reauth_state"', source)
        self.assertIn('path: "/api/auth/oidc/callback"', source)
        self.assertIn('httpOnly: true', source)
        self.assertIn('secure: true', source)

    def test_callback_replaces_main_cookie_only_after_valid_backend_rotation(self):
        source = (ROOT / "web/app/api/auth/oidc/callback/route.ts").read_text(encoding="utf-8")
        replacement = source.index('response.cookies.set("ai_etl_token", data.token')
        backend_success = source.index("if (!upstream.ok)")
        token_validation = source.index('!data.token.startsWith("ps1_")')
        self.assertLess(backend_success, replacement)
        self.assertLess(token_validation, replacement)
        self.assertNotIn('cookies.set("ai_etl_token"', source[source.index("function reauthenticationFailure"):source.index("function safeReturnPath")])
        self.assertIn('Math.min(30 * 60, remainingSeconds)', source)
        self.assertIn('clearReauthenticationState(response)', source)


if __name__ == "__main__":
    unittest.main()
