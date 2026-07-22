FROM postgres@sha256:7e32e9833a6fb1c92c32552794cb6ed569d51b445a54907d35fc112ef39684db

ARG PGDRILL_INTEGRATION_DEFINITION_SHA

LABEL org.pgdrill.integration.provider="pgbackrest" \
      org.pgdrill.integration.pgbackrest-version="2.58.0" \
      org.pgdrill.integration.postgresql-version="18.3" \
      org.pgdrill.integration.definition-sha="${PGDRILL_INTEGRATION_DEFINITION_SHA}"

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        pgbackrest=2.58.0-1.pgdg13+1 \
    && rm -rf /var/lib/apt/lists/*
