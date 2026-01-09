#!/bin/bash
set -e

# Set defaults if not provided
export POSTGRES_DB=${POSTGRES_DB:-localrecall}
export POSTGRES_USER=${POSTGRES_USER:-localrecall}
export POSTGRES_PASSWORD=${POSTGRES_PASSWORD:-localrecall}

# Initialize PostgreSQL data directory if it doesn't exist
ORIGINAL_PGDATA="$PGDATA"
if [ ! -s "$PGDATA/PG_VERSION" ]; then
    echo "Initializing PostgreSQL database..."
    
    # If the directory exists but we can't write to it, initialize in temp
    USE_TEMP=false
    if [ -d "$PGDATA" ] && [ ! -w "$PGDATA" ]; then
        echo "Data directory exists but is not writable, initializing in temporary location..."
        # Use a unique temp directory with timestamp and random component
        TEMP_DATA="/tmp/postgres-init-$(date +%s)-$$"
        # Clean up any old temp directories
        rm -rf /tmp/postgres-init-* 2>/dev/null || true
        USE_TEMP=true
        PGDATA="$TEMP_DATA"
    fi
    
    # Clean up temp directory if it exists and is not empty
    if [ -d "$PGDATA" ] && [ "$(ls -A $PGDATA 2>/dev/null)" ]; then
        echo "Cleaning up existing temporary directory..."
        rm -rf "$PGDATA"/* "$PGDATA"/.[!.]* "$PGDATA"/..?* 2>/dev/null || true
    fi
    
    # Initialize database
    initdb -D "$PGDATA" \
        --auth-local=trust \
        --auth-host=md5 \
        --encoding=UTF8 \
        --locale=C
    
    # Try to copy to original location if we used temp
    if [ "$USE_TEMP" = true ]; then
        echo "Attempting to copy initialized database to $ORIGINAL_PGDATA..."
        # Remove any existing files in target (if we can)
        rm -rf "$ORIGINAL_PGDATA"/* "$ORIGINAL_PGDATA"/.[!.]* "$ORIGINAL_PGDATA"/..?* 2>/dev/null || true
        # Try to copy
        if cp -a "$PGDATA"/* "$ORIGINAL_PGDATA/" 2>/dev/null; then
            echo "Successfully copied to $ORIGINAL_PGDATA"
            PGDATA="$ORIGINAL_PGDATA"
            rm -rf "$TEMP_DATA"
        else
            echo "Warning: Could not copy to $ORIGINAL_PGDATA, data will be lost on container restart"
            echo "Using temporary location: $PGDATA"
        fi
    fi

    # Configure PostgreSQL
    echo "host all all 0.0.0.0/0 md5" >> "$PGDATA/pg_hba.conf"
    echo "listen_addresses = '*'" >> "$PGDATA/postgresql.conf"
    echo "max_connections = 100" >> "$PGDATA/postgresql.conf"
    echo "shared_buffers = 128MB" >> "$PGDATA/postgresql.conf"
    # TimescaleDB requires shared_preload_libraries
    echo "shared_preload_libraries = 'timescaledb'" >> "$PGDATA/postgresql.conf"

    # Start PostgreSQL temporarily to run init scripts
    echo "Starting PostgreSQL for initialization..."
    pg_ctl -D "$PGDATA" -o "-c listen_addresses='localhost'" -w start

    # Wait for PostgreSQL to be ready
    until pg_isready -U postgres; do
        echo "Waiting for PostgreSQL to start..."
        sleep 1
    done

    # Set password for postgres superuser if provided
    if [ -n "$POSTGRES_PASSWORD" ]; then
        psql -v ON_ERROR_STOP=1 --username postgres <<-EOSQL
            ALTER USER postgres WITH PASSWORD '$POSTGRES_PASSWORD';
EOSQL
    fi

    # Create user and database
    psql -v ON_ERROR_STOP=1 --username postgres <<-EOSQL
        CREATE USER "$POSTGRES_USER" WITH PASSWORD '$POSTGRES_PASSWORD';
        CREATE DATABASE "$POSTGRES_DB" OWNER "$POSTGRES_USER";
        GRANT ALL PRIVILEGES ON DATABASE "$POSTGRES_DB" TO "$POSTGRES_USER";
EOSQL

    # Restart PostgreSQL to load shared_preload_libraries for TimescaleDB
    echo "Restarting PostgreSQL to load TimescaleDB..."
    pg_ctl -D "$PGDATA" -m fast -w stop
    pg_ctl -D "$PGDATA" -o "-c listen_addresses='localhost'" -w start
    
    # Wait for PostgreSQL to be ready again (still using trust auth)
    until pg_isready -U postgres; do
        echo "Waiting for PostgreSQL to restart..."
        sleep 1
    done
    
    # Now update pg_hba.conf to use md5 for local connections after everything is set up
    sed -i 's/local\s\+all\s\+all\s\+trust/local all all md5/g' "$PGDATA/pg_hba.conf"

    # Run initialization scripts
    if [ -d /docker-entrypoint-initdb.d ]; then
        for f in /docker-entrypoint-initdb.d/*.sh; do
            if [ -f "$f" ] && [ -x "$f" ]; then
                echo "Running $f"
                bash "$f"
            fi
        done
    fi

    # Stop PostgreSQL
    pg_ctl -D "$PGDATA" -m fast -w stop
fi

# Start PostgreSQL
exec postgres -D "$PGDATA"
