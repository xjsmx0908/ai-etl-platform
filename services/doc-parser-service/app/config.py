"""Parser Service Configuration"""
from pydantic_settings import BaseSettings
from functools import lru_cache


class Settings(BaseSettings):
    """Application settings"""
    
    # Server
    HOST: str = "0.0.0.0"
    PORT: int = 8000
    
    # Chunking
    MIN_CHUNK_SIZE: int = 128
    MAX_CHUNK_SIZE: int = 4000
    CHUNK_OVERLAP: int = 200
    
    # File limits
    MAX_FILE_SIZE_MB: int = 100
    
    # Logging
    LOG_LEVEL: str = "INFO"
    
    class Config:
        env_file = ".env"
        env_file_encoding = "utf-8"


@lru_cache()
def get_settings() -> Settings:
    """Get cached settings instance"""
    return Settings()
