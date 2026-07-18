FROM python:3.12-alpine

WORKDIR /app

RUN addgroup -S app \
    && adduser -S -G app app \
    && apk add --no-cache su-exec
COPY app.py /app/app.py
COPY entrypoint.sh /usr/local/bin/alert-webhook-entrypoint

RUN chmod 0555 /usr/local/bin/alert-webhook-entrypoint

HEALTHCHECK --interval=10s --timeout=3s --retries=3 \
    CMD python -c "import urllib.request; urllib.request.urlopen('http://localhost:8092/healthz')"

EXPOSE 8092

ENTRYPOINT ["/usr/local/bin/alert-webhook-entrypoint"]
