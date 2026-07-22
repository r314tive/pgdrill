FROM postgres@sha256:7e32e9833a6fb1c92c32552794cb6ed569d51b445a54907d35fc112ef39684db

ARG PGDRILL_INTEGRATION_DEFINITION_SHA

LABEL org.pgdrill.integration.provider="barman" \
      org.pgdrill.integration.barman-version="3.19.1" \
      org.pgdrill.integration.postgresql-version="18.3" \
      org.pgdrill.integration.definition-sha="${PGDRILL_INTEGRATION_DEFINITION_SHA}"

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        barman=3.19.1-3.pgdg13+1 \
        python3-barman=3.19.1-3.pgdg13+1 \
        rsync=3.4.1+ds1-5+deb13u4 \
    && rm -rf /var/lib/apt/lists/*
