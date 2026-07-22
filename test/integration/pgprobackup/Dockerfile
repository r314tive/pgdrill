FROM postgres@sha256:7e32e9833a6fb1c92c32552794cb6ed569d51b445a54907d35fc112ef39684db AS builder

ARG PGDRILL_INTEGRATION_DEFINITION_SHA

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        bison \
        build-essential \
        ca-certificates \
        curl \
        flex \
        git \
        zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

RUN git clone https://github.com/postgrespro/pg_probackup.git \
    && cd pg_probackup \
    && git checkout --detach 79b986494ecea8bbd67f97a62ba8ae4a00703586 \
    && test "$(git rev-parse HEAD)" = "79b986494ecea8bbd67f97a62ba8ae4a00703586"

RUN curl \
        --fail \
        --location \
        --proto '=https' \
        --retry 5 \
        --retry-all-errors \
        --show-error \
        --silent \
        --tlsv1.2 \
        --output postgresql-18.3.tar.bz2 \
        https://ftp.postgresql.org/pub/source/v18.3/postgresql-18.3.tar.bz2 \
    && printf '%s  %s\n' \
        d95663fbbf3a80f81a9d98d895266bdcb74ba274bcc04ef6d76630a72dee016f \
        postgresql-18.3.tar.bz2 | sha256sum --check - \
    && tar -xjf postgresql-18.3.tar.bz2 \
    && patch --directory=postgresql-18.3 --strip=1 \
        < pg_probackup/patches/REL_18_STABLE_pg_probackup.patch

RUN cp --archive pg_probackup postgresql-18.3/contrib/pg_probackup \
    && cd postgresql-18.3 \
    && ./configure \
        --prefix=/opt/postgresql-18.3 \
        --with-zlib \
        --without-icu \
        --without-readline \
    && make --jobs="$(nproc)" \
    && make --directory=contrib/amcheck \
    && make --directory=contrib/pg_probackup \
    && make DESTDIR=/stage install \
    && make --directory=contrib/amcheck DESTDIR=/stage install \
    && make --directory=contrib/pg_probackup DESTDIR=/stage install

FROM postgres@sha256:7e32e9833a6fb1c92c32552794cb6ed569d51b445a54907d35fc112ef39684db

ARG PGDRILL_INTEGRATION_DEFINITION_SHA

LABEL org.pgdrill.integration.provider="pg_probackup" \
      org.pgdrill.integration.pg-probackup-version="2.5.16" \
      org.pgdrill.integration.pg-probackup-commit="79b986494ecea8bbd67f97a62ba8ae4a00703586" \
      org.pgdrill.integration.postgresql-version="18.3" \
      org.pgdrill.integration.postgresql-source-sha256="d95663fbbf3a80f81a9d98d895266bdcb74ba274bcc04ef6d76630a72dee016f" \
      org.pgdrill.integration.definition-sha="${PGDRILL_INTEGRATION_DEFINITION_SHA}"

COPY --from=builder /stage/opt/postgresql-18.3 /opt/postgresql-18.3

ENV PATH=/opt/postgresql-18.3/bin:$PATH
