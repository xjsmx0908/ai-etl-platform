import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


class LoginErrorLocalizationTests(unittest.TestCase):
    def test_login_page_localizes_api_credentials_error(self):
        helper = (ROOT / "web" / "lib" / "loginErrors.ts").read_text(encoding="utf-8")
        page = (ROOT / "web" / "app" / "login" / "page.tsx").read_text(encoding="utf-8")
        route = (ROOT / "web" / "app" / "api" / "auth" / "login" / "route.ts").read_text(encoding="utf-8")
        self.assertIn('"invalid credentials": "用户名或密码错误"', helper)
        self.assertIn('"user is inactive": "账号已停用"', helper)
        self.assertIn("export function localizeLoginError", helper)
        self.assertIn("localizeLoginError", page)
        self.assertNotIn('setError((err as Error).message || "登录失败")', page)
        self.assertIn("localizeLoginError", route)
        self.assertNotIn("? ((data as { error: string }).error as string)", route)


if __name__ == "__main__":
    unittest.main()
