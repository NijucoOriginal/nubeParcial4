# CompuNube — HTTPaaS Local
## Guía de configuración paso a paso

---

## 1. Requisitos previos

- Oracle VirtualBox 7.x instalado en Windows
- GoLand o cualquier IDE con soporte para Go
- Go 1.21 o superior instalado

---

## 2. Configuración de las máquinas virtuales

### 2.1 Máquina virtual ns (Servidor DNS)

Esta VM actúa como servidor DNS autoritativo con Bind9.

**Configuración de red en VirtualBox:**
- Adaptador 1: Red interna → nombre: `intnet`
- Adaptador 2: Red NAT → nombre: `cloudnat`

**Archivo `/etc/network/interfaces`:**
```
auto lo
iface lo inet loopback

auto enp0s3
iface enp0s3 inet static
    address 192.168.10.10
    netmask 255.255.255.0

auto enp0s8
iface enp0s8 inet static
    address 10.10.10.4
    netmask 255.255.255.0
```

Después de editar el archivo ejecuta:
```bash
sudo systemctl restart networking
```

**Requisitos dentro de la VM:**
- Bind9 instalado y configurado con la zona `cloud.local`
- SSH instalado y activo
- Usuario `nicolas` con contraseña `nicolas`
- Sudo sin contraseña: 
```bash
echo "nicolas ALL=(ALL) NOPASSWD:ALL" | sudo tee /etc/sudoers.d/nicolas
```

---

### 2.2 Máquina virtual web3 (Plantilla Apache)

Esta VM es la plantilla base desde la cual se crean nuevas instancias.

**Configuración de red en VirtualBox:**
- Adaptador 1: Red interna → nombre: `intnet`
- Adaptador 2: Red NAT → nombre: `cloudnat`

**Archivo `/etc/network/interfaces` (configuración inicial con disco en modo normal):**
```
auto lo
iface lo inet loopback

auto enp0s3
iface enp0s3 inet static
    address 192.168.10.30
    netmask 255.255.255.0

auto enp0s8
iface enp0s8 inet static
    address 10.10.10.5
    netmask 255.255.255.0
```

> **Importante:** Antes de convertir el disco a multiconexión, debes cambiar `enp0s8` de estático a DHCP para que las nuevas instancias obtengan su propia IP automáticamente. Modifica el archivo así:

```
auto lo
iface lo inet loopback

auto enp0s3
iface enp0s3 inet static
    address 192.168.10.30
    netmask 255.255.255.0

auto enp0s8
iface enp0s8 inet dhcp
```

Esto es necesario porque cada nueva VM creada desde el disco base arrancará con esta configuración y obtendrá su propia IP del servidor DHCP de cloudnat, evitando conflictos de red.

**Requisitos dentro de la VM:**
- Apache2 instalado y activo
- SSH instalado y activo
- `unzip` instalado: `sudo apt install unzip -y`
- Usuario `nicolas` con contraseña `nicolas`
- Sudo sin contraseña:
```bash
echo "nicolas ALL=(ALL) NOPASSWD:ALL" | sudo tee /etc/sudoers.d/nicolas
```
- `PasswordAuthentication yes` en `/etc/ssh/sshd_config`

---

### 2.3 Conversión del disco de web3 a multiconexión

Una vez que web3 tenga todas las configuraciones anteriores correctas:

1. Apaga la VM web3
2. En VirtualBox ve a **Configuración → Almacenamiento**
3. Selecciona el disco `web3-disk1.vdi`
4. Haz clic derecho → **Modificar tipo de disco virtual**
5. Cambia de **Normal** a **Multiconexión**
6. Acepta y guarda

> Desde este momento todas las nuevas VMs creadas por la aplicación usarán este disco como base y heredarán todas las configuraciones que hiciste.

---

## 3. Configuración de la red NAT (cloudnat)

### 3.1 Crear la red NAT

Ejecuta en PowerShell:
```cmd
vboxmanage natnetwork add --netname cloudnat --network "10.10.10.0/24" --enable --dhcp on
```

### 3.2 Configurar reenvío de puertos para ns y web3

```cmd
vboxmanage natnetwork modify --netname cloudnat --port-forward-4 "ns-ssh:tcp:[]:2220:[10.10.10.4]:22"
vboxmanage natnetwork modify --netname cloudnat --port-forward-4 "ns-dns:tcp:[]:5353:[10.10.10.4]:53"
vboxmanage natnetwork modify --netname cloudnat --port-forward-4 "web3-ssh:tcp:[]:2221:[10.10.10.5]:22"
vboxmanage natnetwork modify --netname cloudnat --port-forward-4 "web3-http:tcp:[]:8081:[10.10.10.5]:80"
```

### 3.3 Verificar la red NAT

```cmd
vboxmanage natnetwork list
```

Debes ver las cuatro reglas de reenvío listadas.

---

## 4. Configuración de llaves SSH

Para que la aplicación pueda conectarse automáticamente a las VMs sin pedir contraseña cada vez, debes copiar la llave SSH desde Windows hacia las VMs.

### 4.1 Generar la llave SSH en Windows

```cmd
ssh-keygen -t ed25519 -f "C:\Users\TU_USUARIO\.ssh\id_compunube" -N ""
```

### 4.2 Copiar la llave a ns (puerto 2220)

```cmd
type "C:\Users\TU_USUARIO\.ssh\id_compunube.pub" | ssh -p 2220 nicolas@127.0.0.1 "mkdir -p ~/.ssh && cat >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && chmod 700 ~/.ssh"
```

### 4.3 Copiar la llave a web3 (puerto 2221)

```cmd
type "C:\Users\TU_USUARIO\.ssh\id_compunube.pub" | ssh -p 2221 nicolas@127.0.0.1 "mkdir -p ~/.ssh && cat >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && chmod 700 ~/.ssh"
```

> Reemplaza `TU_USUARIO` por tu nombre de usuario en Windows. En este proyecto es `NICOLAS PEÑA RINCON`.

---

## 5. Configuración del código

### 5.1 Cambiar usuario y contraseña SSH

En el archivo `handlers/handlers.go` busca estas constantes al inicio del archivo y cámbialas por las credenciales de tu VM:

```go
const (
    SSH_USER = "nicolas"   // ← Cambia por tu usuario
    SSH_PASS = "nicolas"   // ← Cambia por tu contraseña
    ...
)
```

### 5.2 Cambiar la ruta del disco base

En el mismo archivo busca esta línea y ajusta la ruta al disco `.vdi` de web3 en tu computador:

```go
BASE_DISK_PATH = `C:\Users\NICOLAS PEÑA RINCON\VirtualBox VMs\web3\web3-disk1.vdi`
```

---

## 6. Ejecutar la aplicación

### 6.1 Ejecutar como administrador

**Es obligatorio ejecutar GoLand o la terminal como administrador.** La aplicación necesita permisos de administrador por dos razones:

1. Para escribir en el archivo `C:\Windows\System32\drivers\etc\hosts` y agregar las entradas DNS de cada instancia creada. Sin esto, los dominios `.cloud.local` no resolverán en el navegador.
2. Para escuchar en el puerto 80 y actuar como proxy inverso hacia las instancias.

Para ejecutar GoLand como administrador: clic derecho en el ícono de GoLand → **Ejecutar como administrador**.

### 6.2 Iniciar el servidor

```cmd
cd nubeParcial4
go run main.go
```

La aplicación estará disponible en `http://localhost:8080`.

---

## 7. Limitación con el reenvío de puertos

### El problema

Cuando se elimina una instancia desde la aplicación, el código intenta eliminar automáticamente las reglas de reenvío de puertos de cloudnat. Sin embargo, en **VirtualBox 7.x** no existe un comando de línea de comandos que permita eliminar reglas de reenvío de puertos de una red NAT. Esta funcionalidad estaba disponible en VirtualBox 6.x pero fue eliminada en versiones posteriores.

### Solución manual

Cada vez que elimines una instancia desde la aplicación, debes eliminar manualmente las reglas de reenvío de esa instancia desde el **Network Manager** de VirtualBox:

1. Abre VirtualBox
2. Ve a **Archivo → Herramientas → Network Manager**
3. Selecciona la pestaña **Redes NAT**
4. Haz clic en **cloudnat**
5. En la pestaña **Reenvío de puertos**, elimina las reglas que correspondan a la instancia eliminada. Por ejemplo, si eliminaste `paisajes`, elimina `paisajes-ssh` y `paisajes-http`
6. Haz clic en **Aplicar**

> La aplicación mostrará un aviso en los logs recordándote hacer esto cada vez que elimines una instancia.

---

## 8. Estructura del proyecto

```
nubeParcial4/
├── main.go              ← Punto de entrada, servidor HTTP y proxy inverso
├── go.mod               ← Dependencias Go
├── handlers/
│   └── handlers.go      ← Toda la lógica de la aplicación
├── static/
│   └── index.html       ← Frontend de la aplicación
└── ip_counter.txt       ← Contador de instancias (se crea automáticamente)
```
