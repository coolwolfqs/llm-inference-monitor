"""Productized core services for the model manager."""

from .catalog import CatalogService
from .config import settings

__all__ = ["CatalogService", "settings"]
