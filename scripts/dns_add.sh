#!/usr/bin/env bash
# dns_add.sh <host> <ip>
# Agrega un registro A en la zona cloud.local del servidor DNS autoritativo (ns1).
set -euo pipefail

HOST="$1"
IP="$2"

NS1_IP="192.168.10.10"
ZONE_FILE="/etc/bind/db.cloud.local"
SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=10"

echo "[INFO] Registrando ${HOST}.cloud.local → ${IP} en ns1..."

ssh $SSH_OPTS usuario@"$NS1_IP" "
    # Incrementar serial (formato: número simple)
    SERIAL=\$(grep -oP '(?<=^\s{0,8})\d{8,}' $ZONE_FILE | head -1)
    NEW_SERIAL=\$(( SERIAL + 1 ))
    sudo sed -i \"s/\$SERIAL/\$NEW_SERIAL/\" $ZONE_FILE

    # Agregar registro A si no existe
    if ! grep -q '^${HOST}[[:space:]]' $ZONE_FILE; then
        echo '${HOST}    IN  A   ${IP}' | sudo tee -a $ZONE_FILE > /dev/null
    fi

    # Recargar zona bind9
    sudo rndc reload cloud.local
"

echo "[INFO] DNS registrado: ${HOST}.cloud.local → ${IP}"
