#!/usr/bin/env python3
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


class DemoLoginContractTests(unittest.TestCase):
    def test_login_page_offers_one_click_demo_accounts(self):
        page = (ROOT / "web/app/login/page.tsx").read_text(encoding="utf-8")
        self.assertIn("体验问答（普通账号）", page)
        self.assertIn("体验发布预审（管理员）", page)
        self.assertIn('demoLogin(account)', page)
        self.assertIn('"/release-center"', page)
        self.assertIn('"/qa"', page)
        self.assertNotIn("BOOTSTRAP_ADMIN", page)
        self.assertNotIn("placeholder=\"demo-admin\"", page)

    def test_web_client_uses_demo_login_endpoint(self):
        client = (ROOT / "web/lib/apiClient.ts").read_text(encoding="utf-8")
        route = (ROOT / "web/app/api/auth/demo-login/route.ts").read_text(encoding="utf-8")
        self.assertIn('"/auth/demo-login"', client)
        self.assertIn('account: "user" | "admin"', client)
        self.assertIn("/v1/auth/demo-login", route)
        self.assertIn("ai_etl_token", route)

    def test_demo_showcase_seeds_release_center_samples(self):
        source = (ROOT / "services/etl-worker/cmd/api/demo_showcase.go").read_text(encoding="utf-8")
        self.assertIn("demo-doc-onboarding", source)
        self.assertIn("demo-doc-payroll", source)
        self.assertIn("sensitive_data_detected", source)
        self.assertIn("approval_pending", source)
        self.assertNotIn("BOOTSTRAP_ADMIN", source)


if __name__ == "__main__":
    unittest.main()
