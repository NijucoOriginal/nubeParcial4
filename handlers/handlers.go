package handlers

import (
	"archive/zip"
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
	used := map[string]bool{}
	for _, inst := range instances {
		used[inst.IP] = true
	}
	for i := 32; i < 250; i++ {
		ip := fmt.Sprintf("192.168.10.%d", i)
		if !used[ip] {
			return ip, nil
		}
	}
	return "", fmt.Errorf("no hay IPs disponibles en el rango")
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

	// Crear VM
	if _, err := vbox("createvm", "--name", vmName, "--ostype", "Debian_64", "--register"); err != nil {
		return fmt.Errorf("createvm: %w", err)
	}

	// Configurar hardware — solo red interna
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

	// Controlador SATA
	if _, err := vbox("storagectl", vmName, "--name", "SATA Controller", "--add", "sata", "--controller", "IntelAhci"); err != nil {
		return fmt.Errorf("storagectl: %w", err)
	}

	// Adjuntar disco multiconexión
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

	// Arrancar headless
	if _, err := vbox("startvm", vmName, "--type", "headless"); err != nil {
		return fmt.Errorf("startvm: %w", err)
	}

	// Esperar que arranque
	log.Printf("[INFO] Esperando que '%s' arranque...", vmName)
	time.Sleep(25 * time.Second)

	// Configurar hostname e IP via SSH a web3 (la nueva VM arranca con la misma IP de web3)
	configCmd := fmt.Sprintf(`
sudo hostnamectl set-hostname %s.cloud.local
sudo tee /etc/network/interfaces > /dev/null <<'EOF'
auto lo
iface lo inet loopback
auto enp0s3
iface enp0s3 inet static
    address %s
    netmask %s
    gateway %s
    dns-nameservers %s
EOF
sudo tee /etc/resolv.conf > /dev/null <<'EOF2'
nameserver %s
EOF2
sudo systemctl restart networking
sudo systemctl restart apache2
`, host+".cloud.local", ip, NETMASK, GATEWAY, DNS_INTERNAL, DNS_INTERNAL)

	if _, err := sshRun(WEB3_IP, WEB3_PORT, configCmd); err != nil {
		return fmt.Errorf("ssh config: %w", err)
	}

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
	// Esperar SSH en la nueva instancia (IP interna)
	log.Printf("[INFO] Esperando SSH en %s...", ip)
	var sshReady bool
	for i := 0; i < 12; i++ {
		conn, err := net.DialTimeout("tcp", ip+":22", 3*time.Second)
		if err == nil {
			conn.Close()
			sshReady = true
			break
		}
		time.Sleep(5 * time.Second)
	}
	if !sshReady {
		return fmt.Errorf("la instancia %s no respondió SSH en 60s", ip)
	}

	// Leer zip y extraer index.html y demás archivos
	webRoot := "/var/www/html/" + host

	// Crear directorio y configurar Apache via SSH
	setupCmd := fmt.Sprintf(`sudo mkdir -p %s && sudo chown -R www-data:www-data %s`, webRoot, webRoot)
	if _, err := sshRun(ip, "22", setupCmd); err != nil {
		return fmt.Errorf("error creando directorio web: %w", err)
	}

	// Leer y transferir archivos del zip via SSH
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("error abriendo zip: %w", err)
	}
	defer r.Close()

	config := &ssh.ClientConfig{
		User:            SSH_USER,
		Auth:            []ssh.AuthMethod{ssh.Password("nicolas")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}
	client, err := ssh.Dial("tcp", ip+":22", config)
	if err != nil {
		return fmt.Errorf("error SSH a nueva instancia: %w", err)
	}
	defer client.Close()

	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		content, _ := io.ReadAll(rc)
		rc.Close()

		destPath := webRoot + "/" + f.Name
		session, err := client.NewSession()
		if err != nil {
			continue
		}
		stdin, _ := session.StdinPipe()
		session.Start(fmt.Sprintf("sudo tee %s > /dev/null", destPath))
		stdin.Write(content)
		stdin.Close()
		session.Wait()
		session.Close()
	}

	// Configurar VirtualHost Apache
	vhostCmd := fmt.Sprintf(`
sudo tee /etc/apache2/sites-available/%s.conf > /dev/null <<'EOF'
<VirtualHost *:80>
    ServerName %s.cloud.local
    DocumentRoot %s
    <Directory %s>
        Options Indexes FollowSymLinks
        AllowOverride All
        Require all granted
    </Directory>
</VirtualHost>
EOF
sudo a2ensite %s.conf
sudo a2dissite 000-default.conf 2>/dev/null || true
sudo systemctl reload apache2
`, host, host, webRoot, webRoot, host)

	if _, err := sshRun(ip, "22", vhostCmd); err != nil {
		return fmt.Errorf("error configurando Apache: %w", err)
	}

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
