import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


class ReleaseCenterFetchErrorTests(unittest.TestCase):
    def test_quiet_refresh_does_not_paint_raw_failed_to_fetch(self):
        page = (ROOT / "web" / "app" / "(app)" / "agent" / "page.tsx").read_text(encoding="utf-8")
        helper = (ROOT / "web" / "lib" / "fetchErrors.ts").read_text(encoding="utf-8")

        self.assertIn("refreshReleaseCenter(true)", page)
        self.assertIn("localizeFetchError", page)
        self.assertIn("isTransientFetchError", page)
        self.assertIn('setError("")', page)
        self.assertNotIn(
            'setError((e as Error).message || "无法刷新发布中心")',
            page,
        )
        self.assertIn("Failed to fetch", helper)
        self.assertIn("Fail to fetch", helper)
        self.assertIn("fetch failed", helper)
        self.assertIn("同步暂时失败，请稍后重试", helper)
        self.assertIn("export function localizeFetchError", helper)
        self.assertIn("export function isTransientFetchError", helper)

    def test_release_center_bff_catches_upstream_fetch_failures(self):
        proxy = (ROOT / "web" / "lib" / "backendProxy.ts").read_text(encoding="utf-8")
        self.assertIn("upstream temporarily unavailable", proxy)
        self.assertIn("503", proxy)
        self.assertIn("export async function proxyBackend", proxy)

        for relative in (
            "web/app/api/release-center/overview/route.ts",
            "web/app/api/release-center/requests/route.ts",
            "web/app/api/documents/route.ts",
        ):
            source = (ROOT / relative).read_text(encoding="utf-8")
            self.assertIn("proxyBackend", source, relative)
            self.assertNotRegex(
                source,
                r"const upstream = await fetch\(",
                msg=f"{relative} still calls fetch without the shared proxy",
            )

        review = (ROOT / "web/app/api/release-center/review-reports/[id]/route.ts").read_text(encoding="utf-8")
        self.assertIn("upstream temporarily unavailable", review)
        self.assertIn("catch", review)


if __name__ == "__main__":
    unittest.main()
