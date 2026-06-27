from functools import lru_cache
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", env_file_encoding="utf-8")

    HOST: str = "0.0.0.0"
    PORT: int = 8091
    ENVIRONMENT: str = "dev"
    LOG_LEVEL: str = "INFO"

    RERANKER_BACKEND: str = "cross-encoder"
    RERANKER_MODEL: str = "cross-encoder/ms-marco-MiniLM-L6-v2"
    RERANKER_DEVICE: str = "cpu"
    RERANKER_BATCH_SIZE: int = 16
    RERANKER_MAX_DOCUMENTS: int = 100
    RERANKER_MAX_DOCUMENT_CHARS: int = 12000
    RERANKER_LOAD_ON_STARTUP: bool = True


@lru_cache()
def get_settings() -> Settings:
    return Settings()
