"""FastAPI Application Entry Point"""
from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from loguru import logger
import sys

from app.config import get_settings
from app.routers import parse
from app.models import HealthResponse

# Configure logging
logger.remove()
logger.add(
    sys.stderr,
    level=get_settings().LOG_LEVEL,
    format="<green>{time:YYYY-MM-DD HH:mm:ss}</green> | <level>{level: <8}</level> | <cyan>{name}</cyan>:<cyan>{function}</cyan> - <level>{message}</level>"
)

# Create FastAPI app
app = FastAPI(
    title="Document Parser Service",
    description="Enterprise document parsing and semantic chunking service",
    version="1.0.0"
)

# CORS middleware
app.add_middleware(
    CORSMiddleware,
    allow_origins=get_settings().cors_allowed_origins,
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

# Include routers
app.include_router(parse.router)


@app.on_event("startup")
async def validate_security_config():
    """Fail fast on insecure production/staging configuration."""
    settings = get_settings()
    if settings.ENVIRONMENT.lower() != "dev" and not settings.resolved_internal_api_token:
        raise RuntimeError("INTERNAL_API_TOKEN or INTERNAL_API_TOKEN_FILE is required when ENVIRONMENT is not dev")
    if settings.ENVIRONMENT.lower() != "dev" and "*" in settings.cors_allowed_origins:
        raise RuntimeError("CORS_ALLOWED_ORIGINS must not contain '*' when ENVIRONMENT is not dev")


@app.get("/healthz", response_model=HealthResponse)
async def health_check():
    """Health check endpoint"""
    return HealthResponse(status="healthy")


@app.get("/readyz")
async def readiness_check():
    """Readiness check endpoint"""
    return {"status": "ready"}


@app.get("/")
async def root():
    """Root endpoint"""
    return {
        "service": "Document Parser Service",
        "version": "1.0.0",
        "docs": "/docs",
        "health": "/healthz"
    }


if __name__ == "__main__":
    import uvicorn
    
    settings = get_settings()
    
    logger.info(f"Starting Parser Service on {settings.HOST}:{settings.PORT}")
    
    uvicorn.run(
        "app.main:app",
        host=settings.HOST,
        port=settings.PORT,
        reload=True if settings.LOG_LEVEL == "DEBUG" else False,
        log_level=settings.LOG_LEVEL.lower()
    )
