#!/usr/bin/env bash
# Install cfgsync on a fresh Ubuntu/Debian VPS.
# Run as root: sudo bash install.sh
set -euo pipefail

REPO="github.com/viccom/cfgsync"
VERSION="${VERSION:-v0.1.0}"
INSTALL_DIR="/opt/cfgsync"
DATA_DIR="$INSTALL_DIR/data"
BACKUP_DIR="$INSTALL_DIR/backups"
ETC_DIR="/etc/cfgsync"
SERVICE_USER="cfgsync"
DEFAULT_PORT="28972"

echo "==> installing prerequisites"
apt-get update
apt-get install -y --no-install-recommends sqlite3 ca-certificates

echo "==> creating user and directories"
id "$SERVICE_USER" >/dev/null 2>&1 || useradd --system --shell /usr/sbin/nologin --home-dir "$INSTALL_DIR" "$SERVICE_USER"
mkdir -p "$INSTALL_DIR/bin" "$DATA_DIR" "$BACKUP_DIR" "$ETC_DIR"
chown -R "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR"

echo "==> downloading $VERSION"
url="https://github.com/viccom/cfgsync/releases/download/$VERSION/cfgsync-linux-amd64"
curl -fL "$url" -o "$INSTALL_DIR/bin/cfgsync"
chmod +x "$INSTALL_DIR/bin/cfgsync"
ln -sf "$INSTALL_DIR/bin/cfgsync" "$INSTALL_DIR/cfgsync"

echo "==> generating JWT_SECRET"
secret="$(openssl rand -hex 32)"
cat > "$ETC_DIR/env" <<EOF
JWT_SECRET=$secret
DB_PATH=$DATA_DIR/cloud.db
LISTEN=:$DEFAULT_PORT
EOF
chmod 600 "$ETC_DIR/env"
chown -R "$SERVICE_USER:$SERVICE_USER" "$ETC_DIR"

echo "==> installing systemd unit"
cp "$(dirname "$0")/../scripts/cfgsync.service" /etc/systemd/system/cfgsync.service
systemctl daemon-reload
systemctl enable --now cfgsync

echo "==> installing backup cron"
cat > /etc/cron.d/cfgsync-backup <<EOF
0 3 * * * $SERVICE_USER DB_PATH=$DATA_DIR/cloud.db BACKUP_DIR=$BACKUP_DIR $INSTALL_DIR/scripts/backup.sh
EOF
chmod 644 /etc/cron.d/cfgsync-backup
mkdir -p "$INSTALL_DIR/scripts"
cp "$(dirname "$0")/../scripts/backup.sh" "$INSTALL_DIR/scripts/backup.sh"
chmod +x "$INSTALL_DIR/scripts/backup.sh"

echo
echo "DONE. Service is running on http://127.0.0.1:$DEFAULT_PORT"
echo "Set up Caddy reverse proxy (see deploy/Caddyfile) to expose HTTPS."
echo
echo "Verify:  curl http://127.0.0.1:$DEFAULT_PORT/api/v1/health"
echo "         -> {\"status\":\"ok\",\"db\":\"ok\"}"
