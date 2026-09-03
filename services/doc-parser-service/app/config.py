"""Parser Service Configuration"""
from functools import lru_cache
from typing import Optional
from pathlib import Path
from pydantic_settings import BaseSettings


def resolve_secret(value: Optional[str], file_path: Optional[str]) -> Optional[str]:
    """Resolve secret from value or *_FILE path (value has higher priority)."""
    if value:
        return value
    if not file_path:
        return None
    try:
        secret = Path(file_path).read_text(encoding="utf-8").strip()
    except OSError:
        return None
    return secret or None


def parse_csv(raw: str) -> list[str]:
    values = [item.strip() for item in raw.split(",")]
    return [item for item in values if item]


class Settings(BaseSettings):
    """Application settings"""
    
    # Server
    HOST: str = "0.0.0.0"
    PORT: int = 8000
    ENVIRONMENT: str = "dev"
    
    # Chunking
    MIN_CHUNK_SIZE: int = 128
    MAX_CHUNK_SIZE: int = 4000
    CHUNK_OVERLAP: int = 200
    
    # File limits
    MAX_FILE_SIZE_MB: int = 100
    # Retained as an operational hint for deployments; batching is enforced by
    # the Worker, so this value never rejects a multi-hundred-page document.
    MAX_OCR_PAGES: int = 0
    
    # Logging
    LOG_LEVEL: str = "INFO"

    # CORS
    CORS_ALLOWED_ORIGINS: str = "*"

    # Internal service-to-service auth
    INTERNAL_API_TOKEN: Optional[str] = None
    INTERNAL_API_TOKEN_FILE: Optional[str] = None
    
    class Config:
        env_file = ".env"
        env_file_encoding = "utf-8"

    @property
    def resolved_internal_api_token(self) -> Optional[str]:
        return resolve_secret(self.INTERNAL_API_TOKEN, self.INTERNAL_API_TOKEN_FILE)

    @property
    def cors_allowed_origins(self) -> list[str]:
        return parse_csv(self.CORS_ALLOWED_ORIGINS)


@lru_cache()
def get_settings() -> Settings:
    """Get cached settings instance"""
    return Settings()
