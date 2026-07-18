FROM prom/alertmanager:v0.33.1

USER root

COPY entrypoint.sh /usr/local/bin/alertmanager-entrypoint
RUN chmod 0555 /usr/local/bin/alertmanager-entrypoint

ENTRYPOINT ["/usr/local/bin/alertmanager-entrypoint"]
CMD ["--storage.path=/alertmanager"]
