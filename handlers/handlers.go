package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ─── Config ───────────────────────────────────────────────────────────────────

const (
	NS1_IP       = "192.168.1.12"
	NS1_PORT     = "22"
	WEB3_IP      = "192.168.1.13"
	WEB3_PORT    = "22"
	SSH_USER     = "nicolas"
	ZONE_FILE    = "/etc/bind/db.cloud.local"
	INTERNAL_NET = "intnet"
	DNS_INTERNAL = "192.168.10.10"
	GATEWAY      = "192.168.10.1"
	NETMASK      = "255.255.255.0"
)

const IP_COUNTER_FILE = "/tmp/ip_counter.txt"

var lastIP int

// Ruta al disco .vdi de web3 — ajusta si es diferente
var BASE_DISK_PATH = `C:\Users\NICOLAS PEÑA RINCON\VirtualBox VMs\web3\web3-disk1.vdi`

// ─── Models ───────────────────────────────────────────────────────────────────

type Instance struct {
	Host      string    `json:"host"`
	IP        string    `json:"ip"`
	URL       string    `json:"url"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

type StatusInfo struct {
	VirtualBox string `json:"virtualbox"`
	VBoxDetail string `json:"vbox_detail"`
	DNS        string `json:"dns"`
	DNSDetail  string `json:"dns_detail"`
	Apache     string `json:"apache"`
	ApacheMsg  string `json:"apache_msg"`
}

type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
}

// ─── In-memory store ──────────────────────────────────────────────────────────

var (
	mu        sync.Mutex
	instances = []Instance{}
	logs      = []LogEntry{}
)

func addLog(level, msg string) {
	entry := LogEntry{
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		Level:     level,
		Message:   msg,
	}
	logs = append(logs, entry)
	log.Printf("[%s] %s", level, msg)
}

// ─── IP allocation ────────────────────────────────────────────────────────────

func nextAvailableIP() (string, error) {
	lastIP++
	if lastIP > 254 {
		return "", fmt.Errorf("no hay IPs disponibles en el rango")
	}
	ip := fmt.Sprintf("192.168.10.%d", lastIP)

	// Guardar el nuevo valor en web3
	go sshRun(WEB3_IP, WEB3_PORT, fmt.Sprintf("echo %d > %s", lastIP, IP_COUNTER_FILE))

	return ip, nil
}

// ─── SSH helper ───────────────────────────────────────────────────────────────

func sshRun(host, port, cmd string) (string, error) {
	config := &ssh.ClientConfig{
		User: SSH_USER,
		Auth: []ssh.AuthMethod{
			ssh.Password("nicolas"), // reemplaza por tu contraseña real
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}
	client, err := ssh.Dial("tcp", host+":"+port, config)
	if err != nil {
		return "", fmt.Errorf("error conectando SSH a %s: %w", host, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("error creando sesión SSH: %w", err)
	}
	defer session.Close()

	out, err := session.CombinedOutput(cmd)
	return string(out), err
}

// ─── VBoxManage helper ────────────────────────────────────────────────────────

func vbox(args ...string) (string, error) {
	cmd := exec.Command("vboxmanage", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ─── Helpers HTTP ─────────────────────────────────────────────────────────────

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	corsHeaders(w)
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// ─── GET /api/status ──────────────────────────────────────────────────────────

func GetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		return
	}

	s := StatusInfo{}

	// VirtualBox
	out, err := exec.Command("vboxmanage", "list", "runningvms").CombinedOutput()
	if err != nil {
		s.VirtualBox = "ERROR"
		s.VBoxDetail = "VBoxManage no encontrado"
	} else {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		count := 0
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				count++
			}
		}
		s.VirtualBox = "OK"
		s.VBoxDetail = fmt.Sprintf("%d VM(s) corriendo", count)
	}

	// DNS
	connDNS, errDNS := net.DialTimeout("tcp", NS1_IP+":53", 2*time.Second)
	if errDNS != nil {
		s.DNS = "ERROR"
		s.DNSDetail = "ns1 no responde en puerto 53"
	} else {
		connDNS.Close()
		s.DNS = "OK"
		s.DNSDetail = "ns1.cloud.local activo"
	}

	// Apache
	connWeb, errWeb := net.DialTimeout("tcp", WEB3_IP+":80", 2*time.Second)
	if errWeb != nil {
		s.Apache = "ERROR"
		s.ApacheMsg = "Plantilla base no encontrada"
	} else {
		connWeb.Close()
		s.Apache = "OK"
		s.ApacheMsg = "web3.cloud.local activo"
	}

	writeJSON(w, http.StatusOK, s)
}

// ─── GET /api/instances ───────────────────────────────────────────────────────

func GetInstances(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"instances": instances,
		"logs":      logs,
	})
}

// ─── POST /api/provision ─────────────────────────────────────────────────────

func Provision(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "método no permitido"})
		return
	}

	if err := r.ParseMultipartForm(50 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "error al parsear formulario"})
		return
	}

	host := strings.ToLower(strings.TrimSpace(r.FormValue("host")))
	host = strings.ReplaceAll(host, " ", "-")
	if host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nombre de host requerido"})
		return
	}

	file, fh, err := r.FormFile("zip")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "archivo zip requerido"})
		return
	}
	defer file.Close()

	if !strings.HasSuffix(strings.ToLower(fh.Filename), ".zip") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "el archivo debe ser .zip"})
		return
	}

	mu.Lock()
	for _, inst := range instances {
		if inst.Host == host {
			mu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]string{"error": "ya existe una instancia con ese host"})
			return
		}
	}
	ip, err := nextAvailableIP()
	if err != nil {
		mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	mu.Unlock()

	// Guardar zip
	zipPath := filepath.Join(os.TempDir(), fh.Filename)
	dst, err := os.Create(zipPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no se pudo guardar el zip"})
		return
	}
	io.Copy(dst, file)
	dst.Close()

	go func() {
		mu.Lock()
		addLog("INFO", fmt.Sprintf("Iniciando aprovisionamiento de '%s.cloud.local' (IP: %s)...", host, ip))
		mu.Unlock()

		// 1. Crear VM
		if err := provisionVM(host, ip); err != nil {
			mu.Lock()
			addLog("ERROR", fmt.Sprintf("Error al crear VM '%s': %v", host, err))
			mu.Unlock()
			return
		}
		mu.Lock()
		addLog("INFO", fmt.Sprintf("Instancia '%s.cloud.local' (IP: %s) aprovisionada con éxito.", host, ip))
		mu.Unlock()

		// 2. Registrar DNS
		if err := dnsAdd(host, ip); err != nil {
			mu.Lock()
			addLog("ERROR", fmt.Sprintf("Error al registrar DNS para '%s': %v", host, err))
			mu.Unlock()
			return
		}
		mu.Lock()
		addLog("INFO", fmt.Sprintf("Registro DNS '%s.cloud.local' → %s creado.", host, ip))
		mu.Unlock()

		// 3. Desplegar zip
		if err := deployZip(host, ip, zipPath); err != nil {
			mu.Lock()
			addLog("ERROR", fmt.Sprintf("Error al desplegar contenido en '%s': %v", host, err))
			mu.Unlock()
			return
		}

		mu.Lock()
		addLog("INFO", fmt.Sprintf("Instancia '%s.cloud.local' (IP: %s) publicada con éxito.", host, ip))
		instances = append(instances, Instance{
			Host:      host,
			IP:        ip + "/24",
			URL:       fmt.Sprintf("http://%s.cloud.local", host),
			State:     "Activo",
			CreatedAt: time.Now(),
		})
		mu.Unlock()
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"message": fmt.Sprintf("Aprovisionamiento de '%s.cloud.local' iniciado.", host),
		"ip":      ip,
	})
}

// ─── Provisioning ─────────────────────────────────────────────────────────────

func provisionVM(host, ip string) error {
	vmName := "apache-" + host

	if _, err := vbox("createvm", "--name", vmName, "--ostype", "Debian_64", "--register"); err != nil {
		return fmt.Errorf("createvm: %w", err)
	}

	if _, err := vbox("modifyvm", vmName,
		"--memory", "512",
		"--cpus", "1",
		"--nic1", "intnet",
		"--intnet1", INTERNAL_NET,
		"--boot1", "disk",
		"--boot2", "none",
		"--boot3", "none",
		"--boot4", "none",
	); err != nil {
		return fmt.Errorf("modifyvm: %w", err)
	}

	if _, err := vbox("storagectl", vmName, "--name", "SATA Controller", "--add", "sata", "--controller", "IntelAhci"); err != nil {
		return fmt.Errorf("storagectl: %w", err)
	}

	if _, err := vbox("storageattach", vmName,
		"--storagectl", "SATA Controller",
		"--port", "0",
		"--device", "0",
		"--type", "hdd",
		"--medium", BASE_DISK_PATH,
		"--mtype", "multiattach",
	); err != nil {
		return fmt.Errorf("storageattach: %w", err)
	}

	// Apagar web3 temporalmente
	log.Printf("[INFO] Apagando web3 temporalmente...")
	vbox("controlvm", "web3", "acpipowerbutton")
	time.Sleep(30 * time.Second)

	if _, err := vbox("startvm", vmName, "--type", "headless"); err != nil {
		return fmt.Errorf("startvm: %w", err)
	}

	log.Printf("[INFO] Esperando que '%s' arranque...", vmName)
	time.Sleep(90 * time.Second)

	// Configurar hostname e IP via túnel ns1 → nueva instancia (red interna)
	config := &ssh.ClientConfig{
		User:            SSH_USER,
		Auth:            []ssh.AuthMethod{ssh.Password("nicolas")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	ns1Client, err := ssh.Dial("tcp", NS1_IP+":"+NS1_PORT, config)
	if err != nil {
		return fmt.Errorf("error SSH a ns1: %w", err)
	}
	defer ns1Client.Close()

	// La nueva VM arrancó con la IP de web3 (192.168.10.30), entramos por ahí
	vmConn, err := ns1Client.Dial("tcp", "192.168.10.30:22")
	if err != nil {
		return fmt.Errorf("error túnel ns1→nueva VM: %w", err)
	}

	vmNConn, chans, reqs, err := ssh.NewClientConn(vmConn, "192.168.10.30:22", config)
	if err != nil {
		return fmt.Errorf("error SSH nueva VM via túnel: %w", err)
	}
	vmClient := ssh.NewClient(vmNConn, chans, reqs)
	defer vmClient.Close()

	configCmd := fmt.Sprintf(`
sudo hostnamectl set-hostname %s.cloud.local
sudo tee /etc/network/interfaces > /dev/null <<'EOF'
auto lo
iface lo inet loopback
auto enp0s3
iface enp0s3 inet static
    address %s
    netmask %s
    dns-nameservers %s
EOF
sudo tee /etc/resolv.conf > /dev/null <<'EOF2'
nameserver %s
EOF2
nohup sudo systemctl restart networking > /dev/null 2>&1 &
nohup sudo systemctl restart apache2 > /dev/null 2>&1 &
`, host+".cloud.local", ip, NETMASK, DNS_INTERNAL, DNS_INTERNAL)

	session, err := vmClient.NewSession()
	if err != nil {
		return fmt.Errorf("error creando sesión en nueva VM: %w", err)
	}
	_, err = session.CombinedOutput(configCmd)
	session.Close()
	if err != nil {
		return fmt.Errorf("error configurando nueva VM: %w", err)
	}

	// Encender web3
	log.Printf("[INFO] Encendiendo web3...")
	vbox("startvm", "web3", "--type", "headless")

	return nil
}

// ─── DNS ──────────────────────────────────────────────────────────────────────

func dnsAdd(host, ip string) error {
	cmd := fmt.Sprintf(`
SERIAL=$(grep -oP '\d+' /etc/bind/db.cloud.local | head -1)
NEW_SERIAL=$((SERIAL + 1))
sudo sed -i "s/$SERIAL/$NEW_SERIAL/" %s
if ! grep -q '^%s[[:space:]]' %s; then
    echo '%s    IN  A   %s' | sudo tee -a %s > /dev/null
fi
sudo rndc reload cloud.local
`, ZONE_FILE, host, ZONE_FILE, host, ip, ZONE_FILE)

	_, err := sshRun(NS1_IP, NS1_PORT, cmd)
	return err
}

func dnsRemove(host string) error {
	cmd := fmt.Sprintf(`
SERIAL=$(grep -oP '\d+' /etc/bind/db.cloud.local | head -1)
NEW_SERIAL=$((SERIAL + 1))
sudo sed -i "s/$SERIAL/$NEW_SERIAL/" %s
sudo sed -i '/^%s[[:space:]]/d' %s
sudo rndc reload cloud.local
`, ZONE_FILE, host, ZONE_FILE)

	_, err := sshRun(NS1_IP, NS1_PORT, cmd)
	return err
}

// ─── Deploy ───────────────────────────────────────────────────────────────────

func deployZip(host, ip, zipPath string) error {
	webRoot := "/var/www/html/" + host

	// Leer el zip localmente
	zipData, err := os.ReadFile(zipPath)
	if err != nil {
		return fmt.Errorf("error leyendo zip: %w", err)
	}

	// Subir el zip a web3 via NS1 como salto intermedio
	log.Printf("[INFO] Subiendo zip a web3 via ns1...")
	config := &ssh.ClientConfig{
		User:            SSH_USER,
		Auth:            []ssh.AuthMethod{ssh.Password("nicolas")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         60 * time.Second,
	}

	// Conectar a ns1
	ns1Client, err := ssh.Dial("tcp", NS1_IP+":"+NS1_PORT, config)
	if err != nil {
		return fmt.Errorf("error SSH a ns1: %w", err)
	}
	defer ns1Client.Close()

	// Desde ns1, abrir túnel hacia web3 por red interna
	web3Conn, err := ns1Client.Dial("tcp", "192.168.10.30:22")
	if err != nil {
		return fmt.Errorf("error abriendo túnel ns1→web3: %w", err)
	}

	// Crear cliente SSH a web3 usando el túnel
	web3NConn, chans, reqs, err := ssh.NewClientConn(web3Conn, "192.168.10.30:22", config)
	if err != nil {
		return fmt.Errorf("error SSH web3 via túnel: %w", err)
	}
	web3Client := ssh.NewClient(web3NConn, chans, reqs)
	defer web3Client.Close()

	// Copiar zip a web3
	log.Printf("[INFO] Copiando zip a web3...")
	session, err := web3Client.NewSession()
	if err != nil {
		return fmt.Errorf("error creando sesión en web3: %w", err)
	}
	stdin, _ := session.StdinPipe()
	session.Start("cat > /tmp/contenido.zip")
	stdin.Write(zipData)
	stdin.Close()
	session.Wait()
	session.Close()

	// Esperar que la nueva instancia aplique su nueva IP
	log.Printf("[INFO] Esperando que la nueva instancia tome IP %s...", ip)
	time.Sleep(30 * time.Second)

	// Verificar desde ns1 que la nueva IP ya responde
	log.Printf("[INFO] Verificando conectividad a %s desde ns1...", ip)
	for i := 0; i < 12; i++ {
		out, _ := sshRun(NS1_IP, NS1_PORT, fmt.Sprintf("ping -c 1 -W 2 %s 2>/dev/null && echo OK || echo FAIL", ip))
		if strings.Contains(out, "OK") {
			log.Printf("[INFO] Nueva instancia %s responde ping.", ip)
			break
		}
		log.Printf("[INFO] Esperando respuesta de %s (%d/12)...", ip, i+1)
		time.Sleep(10 * time.Second)
	}

	apacheConf := fmt.Sprintf(`<VirtualHost *:80>
    ServerName %s.cloud.local
    DocumentRoot %s
    <Directory %s>
        Options Indexes FollowSymLinks
        AllowOverride All
        Require all granted
    </Directory>
</VirtualHost>`, host, webRoot, webRoot)

	// Desde web3, hacer deploy a la nueva instancia por red interna
	deployCmd := fmt.Sprintf(`
for i in $(seq 1 12); do
    ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 nicolas@%s "echo ok" 2>/dev/null && break
    sleep 5
done
scp -o StrictHostKeyChecking=no /tmp/contenido.zip nicolas@%s:/tmp/contenido.zip
ssh -o StrictHostKeyChecking=no nicolas@%s "
    sudo mkdir -p %s &&
    sudo unzip -o /tmp/contenido.zip -d %s &&
    sudo chown -R www-data:www-data %s &&
    sudo tee /etc/apache2/sites-available/%s.conf > /dev/null <<'APACHEEOF'
%s
APACHEEOF
    sudo a2ensite %s.conf &&
    sudo a2dissite 000-default.conf 2>/dev/null || true &&
    sudo systemctl reload apache2
"
`, ip, ip, ip, webRoot, webRoot, webRoot, host, apacheConf, host)

	session2, err := web3Client.NewSession()
	if err != nil {
		return fmt.Errorf("error creando sesión deploy: %w", err)
	}
	out, err := session2.CombinedOutput(deployCmd)
	session2.Close()
	if err != nil {
		return fmt.Errorf("error en deploy desde web3: %s: %w", string(out), err)
	}

	log.Printf("[INFO] Contenido desplegado en http://%s.cloud.local", host)
	return nil
}

// ─── Start / Stop / Delete ───────────────────────────────────────────────────

func StartInstance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "método no permitido"})
		return
	}
	var body struct {
		Host string `json:"host"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	mu.Lock()
	defer mu.Unlock()

	for i, inst := range instances {
		if inst.Host == body.Host {
			if _, err := vbox("startvm", "apache-"+inst.Host, "--type", "headless"); err != nil {
				addLog("ERROR", fmt.Sprintf("Error al encender '%s': %v", inst.Host, err))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no se pudo encender la VM"})
				return
			}
			instances[i].State = "Activo"
			addLog("INFO", fmt.Sprintf("Instancia '%s.cloud.local' encendida.", inst.Host))
			writeJSON(w, http.StatusOK, map[string]string{"message": "instancia encendida"})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "instancia no encontrada"})
}

func StopInstance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "método no permitido"})
		return
	}
	var body struct {
		Host string `json:"host"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	mu.Lock()
	defer mu.Unlock()

	for i, inst := range instances {
		if inst.Host == body.Host {
			if _, err := vbox("controlvm", "apache-"+inst.Host, "acpipowerbutton"); err != nil {
				addLog("ERROR", fmt.Sprintf("Error al apagar '%s': %v", inst.Host, err))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no se pudo apagar la VM"})
				return
			}
			instances[i].State = "Inactivo"
			addLog("INFO", fmt.Sprintf("Instancia '%s.cloud.local' apagada.", inst.Host))
			writeJSON(w, http.StatusOK, map[string]string{"message": "instancia apagada"})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "instancia no encontrada"})
}

func DeleteInstance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "método no permitido"})
		return
	}
	var body struct {
		Host string `json:"host"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	mu.Lock()
	defer mu.Unlock()

	for i, inst := range instances {
		if inst.Host == body.Host {
			// Apagar y eliminar VM
			vbox("controlvm", "apache-"+inst.Host, "poweroff")
			time.Sleep(2 * time.Second)
			vbox("unregistervm", "apache-"+inst.Host, "--delete")

			// Eliminar DNS
			if err := dnsRemove(inst.Host); err != nil {
				addLog("ERROR", fmt.Sprintf("Error eliminando DNS de '%s': %v", inst.Host, err))
			}

			instances = append(instances[:i], instances[i+1:]...)
			addLog("INFO", fmt.Sprintf("Instancia '%s.cloud.local' (IP: %s) eliminada con éxito.", inst.Host, inst.IP))
			writeJSON(w, http.StatusOK, map[string]string{"message": "instancia eliminada"})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "instancia no encontrada"})
}

func LoadIPCounter() {
	out, err := sshRun(WEB3_IP, WEB3_PORT, "cat "+IP_COUNTER_FILE+" 2>/dev/null || echo 30")
	if err != nil {
		log.Printf("[WARN] No se pudo leer contador IP, empezando en 30: %v", err)
		lastIP = 30
		return
	}
	val := strings.TrimSpace(out)
	n := 30
	fmt.Sscanf(val, "%d", &n)
	lastIP = n
	log.Printf("[INFO] Contador IP cargado: siguiente será 192.168.10.%d", lastIP+1)
}

func EnsureInfrastructure() {
	log.Printf("[INFO] Verificando infraestructura...")

	// Obtener lista de VMs corriendo
	out, err := exec.Command("vboxmanage", "list", "runningvms").CombinedOutput()
	if err != nil {
		log.Printf("[WARN] No se pudo verificar VMs corriendo: %v", err)
		return
	}
	running := string(out)

	// Verificar y encender ns1
	if !strings.Contains(running, "\"ns\"") {
		log.Printf("[INFO] ns1 no está corriendo, encendiendo...")
		_, err := vbox("startvm", "ns", "--type", "headless")
		if err != nil {
			log.Printf("[WARN] No se pudo encender ns: %v", err)
		} else {
			log.Printf("[INFO] ns encendida.")
		}
	} else {
		log.Printf("[INFO] ns ya está corriendo.")
	}

	// Verificar y encender web3
	if !strings.Contains(running, "\"web3\"") {
		log.Printf("[INFO] web3 no está corriendo, encendiendo...")
		_, err := vbox("startvm", "web3", "--type", "headless")
		if err != nil {
			log.Printf("[WARN] No se pudo encender web3: %v", err)
		} else {
			log.Printf("[INFO] web3 encendida.")
		}
	} else {
		log.Printf("[INFO] web3 ya está corriendo.")
	}

	// Esperar que los servicios estén listos si se encendieron
	log.Printf("[INFO] Esperando que los servicios estén listos...")
	time.Sleep(20 * time.Second)
}
