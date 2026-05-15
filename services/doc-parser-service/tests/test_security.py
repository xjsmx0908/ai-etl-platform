import pytest
from fastapi import HTTPException

from app.config import get_settings
from app.main import validate_security_config
from app.security import require_internal_token


def clear_settings_cache():
    get_settings.cache_clear()


def test_require_internal_token_dev_without_token_allows_request(monkeypatch):
    monkeypatch.setenv("ENVIRONMENT", "dev")
    monkeypatch.delenv("INTERNAL_API_TOKEN", raising=False)
    clear_settings_cache()

    require_internal_token("")


def test_require_internal_token_staging_rejects_missing_header(monkeypatch):
    monkeypatch.setenv("ENVIRONMENT", "staging")
    monkeypatch.setenv("INTERNAL_API_TOKEN", "parser-secret")
    clear_settings_cache()

    with pytest.raises(HTTPException) as exc:
        require_internal_token("")

    assert exc.value.status_code == 401


def test_require_internal_token_staging_accepts_correct_header(monkeypatch):
    monkeypatch.setenv("ENVIRONMENT", "staging")
    monkeypatch.setenv("INTERNAL_API_TOKEN", "parser-secret")
    clear_settings_cache()

    require_internal_token("parser-secret")


def test_require_internal_token_staging_accepts_token_from_file(monkeypatch, tmp_path):
    token_file = tmp_path / "parser_internal_token"
    token_file.write_text("parser-secret\n", encoding="utf-8")
    monkeypatch.setenv("ENVIRONMENT", "staging")
    monkeypatch.delenv("INTERNAL_API_TOKEN", raising=False)
    monkeypatch.setenv("INTERNAL_API_TOKEN_FILE", str(token_file))
    clear_settings_cache()

    require_internal_token("parser-secret")


@pytest.mark.asyncio
async def test_startup_validation_requires_token_outside_dev(monkeypatch):
    monkeypatch.setenv("ENVIRONMENT", "production")
    monkeypatch.setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com")
    monkeypatch.delenv("INTERNAL_API_TOKEN", raising=False)
    monkeypatch.delenv("INTERNAL_API_TOKEN_FILE", raising=False)
    clear_settings_cache()

    with pytest.raises(RuntimeError):
        await validate_security_config()


@pytest.mark.asyncio
async def test_startup_validation_accepts_token_file_outside_dev(monkeypatch, tmp_path):
    token_file = tmp_path / "parser_internal_token"
    token_file.write_text("parser-secret\n", encoding="utf-8")
    monkeypatch.setenv("ENVIRONMENT", "production")
    monkeypatch.setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com")
    monkeypatch.delenv("INTERNAL_API_TOKEN", raising=False)
    monkeypatch.setenv("INTERNAL_API_TOKEN_FILE", str(token_file))
    clear_settings_cache()

    await validate_security_config()


@pytest.mark.asyncio
async def test_startup_validation_rejects_wildcard_cors_outside_dev(monkeypatch, tmp_path):
    token_file = tmp_path / "parser_internal_token"
    token_file.write_text("parser-secret\n", encoding="utf-8")
    monkeypatch.setenv("ENVIRONMENT", "staging")
    monkeypatch.setenv("CORS_ALLOWED_ORIGINS", "*")
    monkeypatch.delenv("INTERNAL_API_TOKEN", raising=False)
    monkeypatch.setenv("INTERNAL_API_TOKEN_FILE", str(token_file))
    clear_settings_cache()

    with pytest.raises(RuntimeError):
        await validate_security_config()
