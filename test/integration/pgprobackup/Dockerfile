ARG POSTGRES_IMAGE=postgres@sha256:7e32e9833a6fb1c92c32552794cb6ed569d51b445a54907d35fc112ef39684db
FROM ${POSTGRES_IMAGE} AS builder

ARG POSTGRES_IMAGE
ARG POSTGRES_VERSION
ARG POSTGRES_MAJOR
ARG POSTGRES_SOURCE_SHA256
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
        --output "postgresql-${POSTGRES_VERSION}.tar.bz2" \
        "https://ftp.postgresql.org/pub/source/v${POSTGRES_VERSION}/postgresql-${POSTGRES_VERSION}.tar.bz2" \
    && printf '%s  %s\n' \
        "${POSTGRES_SOURCE_SHA256}" \
        "postgresql-${POSTGRES_VERSION}.tar.bz2" | sha256sum --check - \
    && tar -xjf "postgresql-${POSTGRES_VERSION}.tar.bz2" \
    && if [ "${POSTGRES_MAJOR}" = "18" ]; then \
        patch --directory="postgresql-${POSTGRES_VERSION}" --strip=1 \
            < pg_probackup/patches/REL_18_STABLE_pg_probackup.patch; \
    fi

RUN cp --archive pg_probackup "postgresql-${POSTGRES_VERSION}/contrib/pg_probackup" \
    && cd "postgresql-${POSTGRES_VERSION}" \
    && ./configure \
        --prefix=/opt/postgresql \
        --with-zlib \
        --without-icu \
        --without-readline \
    && make --jobs="$(nproc)" \
    && make --directory=contrib/amcheck \
    && make --directory=contrib/pg_probackup \
    && make DESTDIR=/stage install \
    && make --directory=contrib/amcheck DESTDIR=/stage install \
    && make --directory=contrib/pg_probackup DESTDIR=/stage install

FROM ${POSTGRES_IMAGE}

ARG POSTGRES_IMAGE
ARG POSTGRES_VERSION
ARG POSTGRES_SOURCE_SHA256
ARG PGDRILL_INTEGRATION_DEFINITION_SHA

LABEL org.pgdrill.integration.provider="pg_probackup" \
      org.pgdrill.integration.pg-probackup-version="2.5.16" \
      org.pgdrill.integration.pg-probackup-commit="79b986494ecea8bbd67f97a62ba8ae4a00703586" \
      org.pgdrill.integration.postgresql-version="${POSTGRES_VERSION}" \
      org.pgdrill.integration.postgresql-source-sha256="${POSTGRES_SOURCE_SHA256}" \
      org.pgdrill.integration.base-image="${POSTGRES_IMAGE}" \
      org.pgdrill.integration.definition-sha="${PGDRILL_INTEGRATION_DEFINITION_SHA}"

COPY --from=builder /stage/opt/postgresql /opt/postgresql

ENV PATH=/opt/postgresql/bin:$PATH
