#!/usr/bin/env bash
# vm_power.sh <host> <start|stop>
set -euo pipefail

HOST="$1"
ACTION="$2"
VM_NAME="apache-${HOST}"

case "$ACTION" in
    start)
        echo "[INFO] Encendiendo VM $VM_NAME..."
        vboxmanage startvm "$VM_NAME" --type headless
        echo "[INFO] VM $VM_NAME encendida."
        ;;
    stop)
        echo "[INFO] Apagando VM $VM_NAME..."
        vboxmanage controlvm "$VM_NAME" acpipowerbutton
        echo "[INFO] VM $VM_NAME apagada."
        ;;
    *)
        echo "[ERROR] Acción desconocida: $ACTION"
        exit 1
        ;;
esac
