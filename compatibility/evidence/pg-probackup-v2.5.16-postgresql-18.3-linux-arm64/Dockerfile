FROM postgres@sha256:a9abf4275f9e99bff8e6aed712b3b7dfec9cac1341bba01c1ffdfce9ff9fc34a AS builder

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        bison \
        ca-certificates \
        curl \
        flex \
        git \
        zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

RUN git clone https://github.com/postgrespro/pg_probackup.git \
    && cd pg_probackup \
    && git checkout 79b986494ecea8bbd67f97a62ba8ae4a00703586 \
    && test "$(git rev-parse HEAD)" = "79b986494ecea8bbd67f97a62ba8ae4a00703586"

RUN curl --fail --location --silent --show-error \
        --output postgresql-18.3.tar.bz2 \
        https://ftp.postgresql.org/pub/source/v18.3/postgresql-18.3.tar.bz2 \
    && curl --fail --location --silent --show-error \
        --output postgresql-18.3.tar.bz2.sha256 \
        https://ftp.postgresql.org/pub/source/v18.3/postgresql-18.3.tar.bz2.sha256 \
    && sha256sum --check postgresql-18.3.tar.bz2.sha256 \
    && tar -xjf postgresql-18.3.tar.bz2 \
    && patch --directory=postgresql-18.3 --strip=1 \
        < pg_probackup/patches/REL_18_STABLE_pg_probackup.patch

RUN cp --archive pg_probackup postgresql-18.3/contrib/pg_probackup \
    && cd postgresql-18.3 \
    && ./configure \
        --prefix=/opt/postgresql-18.3 \
        --without-icu \
        --without-readline \
    && make --jobs="$(nproc)" \
    && make --directory=contrib/pg_probackup \
    && make \
        DESTDIR=/stage \
        install \
    && make \
        --directory=contrib/pg_probackup \
        DESTDIR=/stage \
        install

FROM postgres@sha256:a9abf4275f9e99bff8e6aed712b3b7dfec9cac1341bba01c1ffdfce9ff9fc34a

COPY --from=builder /stage/opt/postgresql-18.3 /opt/postgresql-18.3

ENV PATH=/opt/postgresql-18.3/bin:$PATH
