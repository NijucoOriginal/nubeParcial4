#!/usr/bin/env bash
# provision.sh <host> <ip> [delete]
# Crea o elimina una VM Apache a partir del disco multiconexión de web1.
set -euo pipefail

HOST="$1"
IP="$2"
ACTION="${3:-create}"

TEMPLATE_VM="web1"
BASE_DISK_PATH="/home/$(whoami)/VirtualBox VMs/web1/web1.vmdk"  # ajustar si difiere
VM_NAME="apache-${HOST}"
INTERNAL_NET="intnet"
GATEWAY="192.168.10.1"
DNS="192.168.10.20"
NETMASK="255.255.255.0"

if [ "$ACTION" = "delete" ]; then
    echo "[INFO] Apagando VM $VM_NAME..."
    vboxmanage controlvm "$VM_NAME" poweroff 2>/dev/null || true
    sleep 2
    echo "[INFO] Eliminando VM $VM_NAME..."
    vboxmanage unregistervm "$VM_NAME" --delete
    echo "[INFO] VM $VM_NAME eliminada."
    exit 0
fi

echo "[INFO] Creando VM $VM_NAME con IP $IP..."

# 1. Crear nueva VM
vboxmanage createvm --name "$VM_NAME" --ostype Debian_64 --register

# 2. Configurar hardware
vboxmanage modifyvm "$VM_NAME" \
    --memory 512 \
    --cpus 1 \
    --nic1 intnet \
    --intnet1 "$INTERNAL_NET" \
    --boot1 disk \
    --boot2 none \
    --boot3 none \
    --boot4 none

# 3. Agregar controlador SATA
vboxmanage storagectl "$VM_NAME" --name "SATA Controller" --add sata --controller IntelAhci

# 4. Adjuntar disco multiconexión (modo multiattach = solo lectura compartida)
vboxmanage storageattach "$VM_NAME" \
    --storagectl "SATA Controller" \
    --port 0 \
    --device 0 \
    --type hdd \
    --medium "$BASE_DISK_PATH" \
    --mtype multiattach

# 5. Arrancar VM headless
vboxmanage startvm "$VM_NAME" --type headless

echo "[INFO] Esperando que la VM arranque..."
sleep 20

# 6. Configurar hostname e IP via SSH (web1 debe tener SSH habilitado en puerto 22)
#    La VM al arrancar tendrá la IP de web1 temporalmente; ajustamos via VBoxManage guest additions
#    o via SSH si ya están instaladas las guest additions.
#    Usamos SSH a web1 (plantilla) para conocer la IP inicial — en multiattach la VM arranca con la misma config.

SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=10"
TEMPLATE_IP="192.168.10.30"

echo "[INFO] Configurando hostname e IP en la nueva instancia..."
ssh $SSH_OPTS usuario@"$TEMPLATE_IP" "
    sudo hostnamectl set-hostname ${HOST}.cloud.local
    sudo tee /etc/network/interfaces > /dev/null <<'EOF'
# Loopback
auto lo
iface lo inet loopback

# Red interna
auto enp0s3
iface enp0s3 inet static
    address ${IP}
    netmask ${NETMASK}
    gateway ${GATEWAY}
    dns-nameservers ${DNS}
EOF
    sudo tee /etc/resolv.conf > /dev/null <<'EOF2'
nameserver ${DNS}
EOF2
    sudo systemctl restart networking
    sudo systemctl restart apache2
"

echo "[INFO] VM $VM_NAME configurada con IP $IP."
