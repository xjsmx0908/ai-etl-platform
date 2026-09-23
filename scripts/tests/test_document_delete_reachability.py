"""Contract test for the decision recorded in `docs/optimization-plan.md` §4.3:
deleting your own upload works over the API but has no button in the web UI.

The decision is a *pair* of facts living on two sides of the stack, and either
side can drift alone:

  - backend: DELETE /v1/documents/{id} is gated on the `upload` scope, and the
    `user` role holds that scope -- so a non-admin really can delete their own
    document over the API;
  - frontend: the delete button renders only when `isAdmin` is true, so that
    same user has no entry point in the UI.

Open the UI and the frontend assertions fail. Tighten the backend to admins and
the backend assertions fail -- at which point §4.3 would be describing a
capability that no longer exists. Either way the failure lands here, which is
the point: the two sides cannot be moved one at a time without someone
re-reading the decision.
"""

import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

HANDLERS = "services/etl-worker/cmd/api/doc_handlers.go"
LOGIN = "services/etl-worker/internal/auth/login.go"
DOCUMENTS_PAGE = "web/app/(app)/documents/page.tsx"
PLAN = "docs/optimization-plan.md"
CONTINUATION = "CONTINUATION.md"


def _read(relative):
    return (ROOT / relative).read_text(encoding="utf-8")


def _delete_arm(source):
    """Slice the `case http.MethodDelete:` arm out of handleDocument's method switch.

    The arm is bounded by the next `case` label rather than by a line number, so
    it survives the surrounding edits that a line-number assertion would not.
    """
    start = source.index("case http.MethodDelete:")
    rest = source[start + 1:]
    nxt = re.search(r"\n[ \t]*case http\.Method", rest)
    if nxt is None:
        raise AssertionError(
            "no case label follows the DELETE arm, so the slice cannot be bounded; "
            "the method switch in handleDocument was restructured"
        )
    return rest[: nxt.start()]


def _cell_containing(page, marker):
    """Return the JSX cell around `marker`, from its `<td` to the closing `</td>`."""
    at = page.index(marker)
    return page[page.rindex("<td", 0, at): page.index("</td>", at)]


class DocumentDeleteReachabilityTests(unittest.TestCase):
    """Locks the §4.3 decision: uploader delete works over the API, not in the UI."""

    def test_backend_delete_arm_is_gated_on_upload_scope(self):
        arm = _delete_arm(_read(HANDLERS))
        self.assertIn("hasScope(", arm)
        self.assertIn('"upload"', arm)

    def test_backend_delete_arm_is_not_admin_only(self):
        # The contrast that makes §4.3 a decision rather than an oversight: the
        # same switch spells admin-only as `!= "admin"` in the PATCH arm below.
        arm = _delete_arm(_read(HANDLERS))
        self.assertNotIn('!= "admin"', arm)

    def test_the_admin_only_spelling_exists_elsewhere_in_the_file(self):
        # Positive control for the assertion above: `assertNotIn` on a string
        # that this file never uses would pass for the wrong reason.
        source = _read(HANDLERS)
        self.assertIn('auth.GetPermission(r.Context()) != "admin"', source)

    def test_user_role_holds_the_upload_scope(self):
        # Without this, "the API can do it" would be false for non-admins and the
        # §4.3 wording would be wrong even though the handler was unchanged.
        source = _read(LOGIN)
        user_arm = source[source.index('case "user":'):]
        user_arm = user_arm[: user_arm.index("default:")]
        self.assertIn("ScopeUpload", user_arm)

    def test_delete_button_renders_only_for_admins(self):
        cell = _cell_containing(_read(DOCUMENTS_PAGE), "onDelete(doc)")
        self.assertIn("isAdmin && (", cell)
        # The call must sit *inside* the guard, not merely next to it: no closing
        # `)}` may appear between the guard and the call it guards.
        guard = cell.index("isAdmin && (")
        call = cell.index("onDelete(doc)")
        self.assertLess(guard, call)
        self.assertNotIn(")}", cell[guard:call])
        # No other role may sneak the button in.
        self.assertNotIn("role ===", cell)
        self.assertNotIn("role !==", cell)

    def test_the_decision_is_recorded_and_both_sides_are_cited(self):
        plan = _read(PLAN)
        section = plan[plan.index("### 4.3 "):]
        section = section[: section.index("### 4.4 ")]
        self.assertIn("仅 API 可用", section)
        # The record is only checkable if it names the two sides it decides between.
        for cited in (HANDLERS, LOGIN, DOCUMENTS_PAGE):
            self.assertIn(cited, section)

    def test_no_document_still_calls_the_decision_pending(self):
        plan = _read(PLAN)
        section = plan[plan.index("### 4.3 "):]
        section = section[: section.index("### 4.4 ")]
        self.assertNotIn("仍需产品决定", section)
        self.assertNotIn("仍需产品决定", _read(CONTINUATION))
        self.assertIn("仅 API 可用", _read(CONTINUATION))


if __name__ == "__main__":
    unittest.main()
