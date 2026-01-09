#!/bin/bash
set -e

# This script runs after the database is initialized
# It creates the necessary extensions
# Must run as postgres superuser to create extensions

# Set defaults if not provided
export POSTGRES_DB=${POSTGRES_DB:-localrecall}
export POSTGRES_USER=${POSTGRES_USER:-localrecall}

echo "Creating extensions in database $POSTGRES_DB..."

# Run as postgres superuser to create extensions
# Note: vectorscale may not be available, but that's okay - the app will fall back to vector
psql --username postgres --dbname "$POSTGRES_DB" <<-EOSQL
    -- Create pg_textsearch extension
    CREATE EXTENSION IF NOT EXISTS pg_textsearch;

    -- Create timescaledb extension first (required for vectorscale)
    CREATE EXTENSION IF NOT EXISTS timescaledb;

    -- Create vectorscale extension (from TimescaleDB) - may not be available
    CREATE EXTENSION IF NOT EXISTS vectorscale CASCADE;

    -- Create vector extension as fallback (pgvector) - always available
    CREATE EXTENSION IF NOT EXISTS vector;
EOSQL

# Check if vectorscale was created, warn if not
if psql -t -A --username postgres --dbname "$POSTGRES_DB" -c "SELECT 1 FROM pg_extension WHERE extname = 'vectorscale'" 2>/dev/null | grep -q 1; then
    echo "vectorscale extension created successfully"
else
    echo "Warning: vectorscale extension not available, will use vector (pgvector) as fallback"
fi

echo "Extensions created successfully"
