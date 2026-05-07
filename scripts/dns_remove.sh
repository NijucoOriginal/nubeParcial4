#!/usr/bin/env bash
# dns_remove.sh <host>
# Elimina el registro A del host en la zona cloud.local del servidor ns1.
set -euo pipefail

HOST="$1"

NS1_IP="192.168.10.10"
ZONE_FILE="/etc/bind/db.cloud.local"
SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=10"

echo "[INFO] Eliminando registro DNS ${HOST}.cloud.local de ns1..."

ssh $SSH_OPTS usuario@"$NS1_IP" "
    # Incrementar serial
    SERIAL=\$(grep -oP '(?<=^\s{0,8})\d{8,}' $ZONE_FILE | head -1)
    NEW_SERIAL=\$(( SERIAL + 1 ))
    sudo sed -i \"s/\$SERIAL/\$NEW_SERIAL/\" $ZONE_FILE

    # Eliminar línea del registro
    sudo sed -i '/^${HOST}[[:space:]]/d' $ZONE_FILE

    # Recargar zona
    sudo rndc reload cloud.local
"

echo "[INFO] Registro DNS ${HOST}.cloud.local eliminado."
