import re
from abc import ABC, abstractmethod

from app.config import Settings
from app.models import RerankResult


class BaseReranker(ABC):
    @property
    @abstractmethod
    def ready(self) -> bool:
        raise NotImplementedError

    @abstractmethod
    def load(self) -> None:
        raise NotImplementedError

    @abstractmethod
    def rerank(self, query: str, documents: list[str], top_n: int) -> list[RerankResult]:
        raise NotImplementedError


class CrossEncoderReranker(BaseReranker):
    def __init__(self, settings: Settings):
        self.settings = settings
        self.model = None

    @property
    def ready(self) -> bool:
        return self.model is not None

    def load(self) -> None:
        if self.model is not None:
            return
        from sentence_transformers import CrossEncoder

        self.model = CrossEncoder(self.settings.RERANKER_MODEL, device=self.settings.RERANKER_DEVICE)

    def rerank(self, query: str, documents: list[str], top_n: int) -> list[RerankResult]:
        self.load()
        pairs = [(query, document) for document in documents]
        scores = self.model.predict(pairs, batch_size=self.settings.RERANKER_BATCH_SIZE)
        return rank_scores(scores, top_n)


class LexicalReranker(BaseReranker):
    @property
    def ready(self) -> bool:
        return True

    def load(self) -> None:
        return None

    def rerank(self, query: str, documents: list[str], top_n: int) -> list[RerankResult]:
        query_tokens = tokenize(query)
        scores = []
        for document in documents:
            doc_tokens = tokenize(document)
            if not query_tokens or not doc_tokens:
                scores.append(0.0)
                continue
            overlap = len(query_tokens & doc_tokens)
            scores.append(overlap / len(query_tokens))
        return rank_scores(scores, top_n)


def build_reranker(settings: Settings) -> BaseReranker:
    backend = settings.RERANKER_BACKEND.strip().lower()
    if backend == "lexical":
        return LexicalReranker()
    return CrossEncoderReranker(settings)


def rank_scores(scores, top_n: int) -> list[RerankResult]:
    normalized = [float(score) for score in scores]
    ranked = sorted(enumerate(normalized), key=lambda item: item[1], reverse=True)
    return [RerankResult(index=index, relevance_score=score) for index, score in ranked[:top_n]]


def tokenize(text: str) -> set[str]:
    return set(re.findall(r"[a-zA-Z0-9]+", text.lower()))

