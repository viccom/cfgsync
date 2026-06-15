#!/usr/bin/env bash
# Backup the SQLite database using the safe .backup command (online).
# Cron: 0 3 * * * /opt/1remote-cloud/scripts/backup.sh
set -euo pipefail

DB="${DB_PATH:-/opt/1remote-cloud/data/cloud.db}"
BACKUP_DIR="${BACKUP_DIR:-/opt/1remote-cloud/backups}"
RETENTION_DAYS="${RETENTION_DAYS:-30}"

mkdir -p "$BACKUP_DIR"

stamp="$(date +%Y%m%d-%H%M%S)"
target="$BACKUP_DIR/cloud-${stamp}.db"

sqlite3 "$DB" ".backup '$target'"

# Verify the backup is non-empty and readable.
if [ ! -s "$target" ]; then
    echo "ERROR: backup is empty: $target" >&2
    exit 1
fi

# Remove backups older than retention.
find "$BACKUP_DIR" -name "cloud-*.db" -mtime "+$RETENTION_DAYS" -delete

echo "$(date -Iseconds): backup ok -> $target"
