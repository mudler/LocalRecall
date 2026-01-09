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
    && rm -rf /var/lib/apt/lists/*

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
