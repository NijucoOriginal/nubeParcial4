# CompuNube — HTTPaaS Local

Aplicación web de gestión de servidores HTTP como servicio, desarrollada en Go.

## Arquitectura

```
Host (esta app Go)
├── ns1.cloud.local  → 192.168.10.10  (DNS autoritativo Bind9)  ← YA CONFIGURADO
├── web1.cloud.local → 192.168.10.30  (plantilla Apache)        ← YA CONFIGURADO
└── apache-<host>    → 192.168.10.32+ (instancias creadas por la app)
```

## Requisitos previos

- Go 1.21+
- VirtualBox + `vboxmanage` en el PATH
- `nc` (netcat) instalado
- SSH configurado sin contraseña (clave pública) hacia ns1 (192.168.10.10) y web1 (192.168.10.30)
- Usuario SSH en las VMs: ajustar `usuario` en los scripts por el usuario real

## Configuración SSH sin contraseña

```bash
ssh-keygen -t ed25519 -f ~/.ssh/id_compunube -N ""
ssh-copy-id -i ~/.ssh/id_compunube.pub usuario@192.168.10.10
ssh-copy-id -i ~/.ssh/id_compunube.pub usuario@192.168.10.30
```

## Ajustes importantes antes de ejecutar

1. En `scripts/provision.sh`, editar:
   - `BASE_DISK_PATH`: ruta real al disco .vmdk multiconexión de web1
   - `usuario`: nombre del usuario SSH en las VMs

2. En todos los scripts, verificar que `usuario` coincida con el usuario de las VMs.

## Ejecutar

```bash
cd compunube
chmod +x scripts/*.sh
go run main.go
```

Abrir en el navegador: http://localhost:8080

## Endpoints API

| Método   | Ruta                      | Descripción                        |
|----------|---------------------------|------------------------------------|
| GET      | /api/status               | Estado de VirtualBox, DNS, Apache  |
| GET      | /api/instances            | Lista instancias + logs            |
| POST     | /api/provision            | Aprovisiona nueva instancia        |
| POST     | /api/instances/start      | Enciende una instancia             |
| POST     | /api/instances/stop       | Apaga una instancia                |
| DELETE   | /api/instances/delete     | Elimina instancia y registro DNS   |

## Flujo de aprovisionamiento

1. Usuario ingresa hostname + archivo .zip → clic Publicar
2. Go invoca `scripts/provision.sh` → crea VM desde disco multiconexión → configura IP/hostname via SSH
3. Go invoca `scripts/dns_add.sh` → agrega registro A en ns1 vía SSH + `rndc reload`
4. Go invoca `scripts/deploy.sh` → copia zip via SCP → extrae en `/var/www/html/<host>` → configura VirtualHost Apache
5. Instancia aparece en el dashboard con URL `http://<host>.cloud.local`
