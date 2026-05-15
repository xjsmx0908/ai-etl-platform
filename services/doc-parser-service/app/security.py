"""Security utilities for internal service-to-service authentication."""
import hmac
from fastapi import Header, HTTPException

from app.config import get_settings


def require_internal_token(x_internal_token: str = Header(default="", alias="X-Internal-Token")):
    """
    Enforce internal API token for parser endpoints.

    Rules:
    - production/staging: token is mandatory
    - dev: token is optional unless INTERNAL_API_TOKEN or INTERNAL_API_TOKEN_FILE is configured
    """
    settings = get_settings()

    expected = settings.resolved_internal_api_token or ""
    enforce = settings.ENVIRONMENT.lower() != "dev" or bool(expected)
    if not enforce:
        return

    if not expected:
        raise HTTPException(status_code=500, detail="internal API token is not configured")

    if not hmac.compare_digest(x_internal_token, expected):
        raise HTTPException(status_code=401, detail="unauthorized")
