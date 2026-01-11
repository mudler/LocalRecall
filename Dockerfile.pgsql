FROM ubuntu:24.04

# Set environment variables to non-interactive to prevent prompts
ENV DEBIAN_FRONTEND=noninteractive
ENV TZ=UTC

# Install necessary packages
RUN apt-get update && \
    apt-get install -y \
        git \
        tzdata \
        gnupg2 \
        wget \
        curl \
        make \
        gcc \
        pkg-config \
        clang \
        libssl-dev \
        lsb-release \
        software-properties-common \
        postgresql-common \
        jq \
        build-essential \
        gosu \
    && rm -rf /var/lib/apt/lists/*

# Create postgres user with specific UID/GID (999:999) for Kubernetes compatibility
# This must be done before installing PostgreSQL packages
# In Kubernetes, set fsGroup: 999 in securityContext to automatically fix volume permissions
RUN if ! getent group postgres > /dev/null 2>&1; then \
        groupadd -r postgres --gid=999; \
    else \
        groupmod -g 999 postgres 2>/dev/null || true; \
    fi && \
    if ! getent passwd postgres > /dev/null 2>&1; then \
        useradd -r -g postgres --uid=999 --home-dir=/var/lib/postgresql --shell=/bin/bash postgres; \
    else \
        usermod -u 999 -g postgres postgres 2>/dev/null || true; \
    fi

# Add the PostgreSQL 18 repository
RUN wget --quiet -O - https://www.postgresql.org/media/keys/ACCC4CF8.asc | apt-key add - && \
    sh -c 'echo "deb http://apt.postgresql.org/pub/repos/apt $(lsb_release -cs)-pgdg main" > /etc/apt/sources.list.d/pgdg.list' && \
    apt-get update


# Install PostgreSQL 18, contrib modules, pgvector, and timescaledb
RUN apt-get install -y \
        postgresql-18 \
        postgresql-contrib-18 \
        postgresql-18-pgvector \
        postgresql-18-timescaledb \
        postgresql-server-dev-18 \
    && rm -rf /var/lib/apt/lists/*

# Ensure PostgreSQL binaries are in the PATH
ENV PATH="/usr/lib/postgresql/18/bin:${PATH}"

# Build and install pg_textsearch extension
RUN git clone https://github.com/timescale/pg_textsearch /tmp/pg_textsearch && \
    cd /tmp/pg_textsearch && \
    make && \
    make install && \
    cd / && \
    rm -rf /tmp/pg_textsearch

# Install Rust (required for pgvectorscale)
# Install with default target for the build platform
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --default-toolchain stable --profile minimal && \
    . $HOME/.cargo/env && \
    rustup default stable && \
    rustup target add $(rustc -vV | grep host | cut -d' ' -f2)
ENV PATH="/root/.cargo/bin:${PATH}"
ENV CARGO_TARGET_DIR="/tmp/cargo-target"

# Build and install pgvectorscale extension (provides diskann access method)
# Note: This may fail with SIGILL errors in some Docker environments due to Rust toolchain issues.
# If this fails, pgvectorscale can be built separately and installed, or the system will fall back to pgvector.
# For production, use a specific version tag instead of main branch.
RUN cd /tmp && \
    git clone --depth 1 https://github.com/timescale/pgvectorscale && \
    cd pgvectorscale/pgvectorscale && \
    PGRX_VERSION=$(cargo metadata --format-version 1 2>/dev/null | jq -r '.packages[] | select(.name == "pgrx") | .version' 2>/dev/null || \
    grep -E 'pgrx\s*=\s*"' Cargo.toml | head -1 | sed -E 's/.*"([^"]+)".*/\1/' || echo "0.11.8") && \
    echo "Installing cargo-pgrx version: $PGRX_VERSION" && \
    cargo install --locked cargo-pgrx --version "$PGRX_VERSION" && \
    cargo pgrx init --pg18 $(which pg_config) && \
    cargo pgrx install --release && \
    echo "Verifying vectorscale extension installation..." && \
    ls -la /usr/lib/postgresql/18/lib/vectorscale*.so 2>/dev/null && \
    ls -la /usr/share/postgresql/18/extension/vectorscale.control 2>/dev/null && \
    echo "Extension files found successfully" || \
    (echo "Warning: Some extension files not found" && \
     find /usr/lib/postgresql/18 -name "*vectorscale*" 2>/dev/null && \
     find /usr/share/postgresql/18 -name "*vectorscale*" 2>/dev/null) && \
    cd / && \
    rm -rf /tmp/pgvectorscale && \
    rm -rf /root/.cargo
# Create directory for init scripts
RUN mkdir -p /docker-entrypoint-initdb.d && \
    chmod 755 /docker-entrypoint-initdb.d

# Copy the initialization script
COPY internal/init-db.sh /docker-entrypoint-initdb.d/01-init-extensions.sh
RUN chmod +x /docker-entrypoint-initdb.d/01-init-extensions.sh

# Create a script to initialize PostgreSQL if needed
COPY internal/postgres-init.sh /usr/local/bin/postgres-init.sh
RUN chmod +x /usr/local/bin/postgres-init.sh

# Set up PostgreSQL data directory
ENV PGDATA=/var/lib/postgresql/data
ENV POSTGRES_DB=localrecall
ENV POSTGRES_USER=localrecall
ENV POSTGRES_PASSWORD=localrecall
RUN mkdir -p "$PGDATA" && \
    chown -R postgres:postgres "$PGDATA" && \
    chmod 700 "$PGDATA"

# Expose PostgreSQL port
EXPOSE 5432

# Use the standard PostgreSQL entrypoint approach
USER postgres

# Initialize database if it doesn't exist, then start PostgreSQL
CMD ["/usr/local/bin/postgres-init.sh"]
