#!/bin/sh
# Each package owns a pristine database: legacy catalogs must not contaminate
# raw bootstrap/extension tests, and outage tests cannot disrupt other packages.
set -eu
python3 test/raw/confinement.py "$(pwd -P)"
packages=$(go list ./rag/... ./pkg/... .)
for package in $packages; do
 sh test/raw/database.sh go test -v -count=1 -p=1 "$package"
done
