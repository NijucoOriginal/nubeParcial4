#!/usr/bin/env bash
# deploy.sh <host> <ip> <zip_path>
# Despliega el contenido del zip en /var/www/html/<host> de la instancia Apache.
set -euo pipefail

HOST="$1"
IP="$2"
ZIP_PATH="$3"

SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=30"
WEB_ROOT="/var/www/html/${HOST}"

echo "[INFO] Esperando que la instancia ${HOST} (${IP}) esté lista..."
# Esperar hasta que SSH responda (máx 60s)
for i in $(seq 1 12); do
    if ssh $SSH_OPTS usuario@"$IP" "echo ok" 2>/dev/null; then
        break
    fi
    sleep 5
done

echo "[INFO] Copiando contenido web a ${HOST} (${IP})..."

# Copiar zip a la VM
scp $SSH_OPTS "$ZIP_PATH" usuario@"$IP":/tmp/contenido.zip

# Desplegar en Apache
ssh $SSH_OPTS usuario@"$IP" "
    # Crear directorio del virtualhost
    sudo mkdir -p ${WEB_ROOT}

    # Descomprimir contenido
    sudo unzip -o /tmp/contenido.zip -d ${WEB_ROOT}
    sudo chown -R www-data:www-data ${WEB_ROOT}
    rm /tmp/contenido.zip

    # Configurar VirtualHost en Apache
    sudo tee /etc/apache2/sites-available/${HOST}.conf > /dev/null <<'EOF'
<VirtualHost *:80>
    ServerName ${HOST}.cloud.local
    DocumentRoot ${WEB_ROOT}
    <Directory ${WEB_ROOT}>
        Options Indexes FollowSymLinks
        AllowOverride All
        Require all granted
    </Directory>
</VirtualHost>
EOF

    sudo a2ensite ${HOST}.conf
    sudo a2dissite 000-default.conf 2>/dev/null || true
    sudo systemctl reload apache2
"

echo "[INFO] Contenido desplegado en http://${HOST}.cloud.local"
