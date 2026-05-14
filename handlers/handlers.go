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
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	SSH_USER     = "nicolas"
	SSH_PASS     = "nicolas"
	ZONE_FILE    = "/etc/bind/db.cloud.local"
	INTERNAL_NET = "intnet"
	NAT_NET      = "cloudnat"

	NS_INTERNAL_IP   = "192.168.10.10"
	WEB3_INTERNAL_IP = "192.168.10.30"
	DNS_INTERNAL     = "192.168.10.10"
	NETMASK_INTERNAL = "255.255.255.0"

	NS_NAT_IP   = "10.10.10.4"
	WEB3_NAT_IP = "10.10.10.5"
	NAT_NETMASK = "255.255.255.0"
	NAT_BASE    = "10.10.10."

	NS_SSH_PORT    = "2220"
	NS_DNS_PORT    = "5353"
	WEB3_SSH_PORT  = "2221"
	WEB3_HTTP_PORT = "8081"

	BASE_SSH_PORT    = 2230
	BASE_HTTP_PORT   = 8090
	BASE_NAT_IP_LAST = 6

	IP_COUNTER_FILE = "ip_counter.txt"
	BASE_DISK_PATH  = `C:\Users\NICOLAS PEÑA RINCON\VirtualBox VMs\web3\web3-disk1.vdi`
)

type Instance struct {
	Host      string    `json:"host"`
	IP        string    `json:"ip"`
	URL       string    `json:"url"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	SSHPort   int       `json:"ssh_port"`
	HTTPPort  int       `json:"http_port"`
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

var (
	mu        sync.Mutex
	instances = []Instance{}
	logs      = []LogEntry{}
	lastIdx   = 0
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

func LoadIPCounter() {
	data, err := os.ReadFile(IP_COUNTER_FILE)
	if err != nil {
		log.Printf("[INFO] Contador no encontrado, empezando en 0")
		lastIdx = 0
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		lastIdx = 0
		return
	}
	lastIdx = n
	log.Printf("[INFO] Contador cargado: %d instancias previas", lastIdx)
}

func saveCounter() {
	os.WriteFile(IP_COUNTER_FILE, []byte(strconv.Itoa(lastIdx)), 0644)
}

func nextPorts() (internalIP string, sshPort, httpPort int) {
	lastIdx++
	saveCounter()
	internalIP = fmt.Sprintf("192.168.10.%d", 30+lastIdx)
	sshPort = BASE_SSH_PORT + lastIdx - 1
	httpPort = BASE_HTTP_PORT + lastIdx - 1
	return
}

func EnsureInfrastructure() {
	log.Printf("[INFO] Verificando infraestructura...")

	out, err := exec.Command("vboxmanage", "list", "runningvms").CombinedOutput()
	if err != nil {
		log.Printf("[WARN] No se pudo verificar VMs: %v", err)
		return
	}
	running := string(out)
	needWait := false

	if !strings.Contains(running, "\"ns\"") {
		log.Printf("[INFO] Encendiendo ns...")
		if _, err := vbox("startvm", "ns", "--type", "headless"); err != nil {
			log.Printf("[WARN] No se pudo encender ns: %v", err)
		} else {
			needWait = true
		}
	} else {
		log.Printf("[INFO] ns ya está corriendo.")
	}

	if !strings.Contains(running, "\"web3\"") {
		log.Printf("[INFO] Encendiendo web3...")
		if _, err := vbox("startvm", "web3", "--type", "headless"); err != nil {
			log.Printf("[WARN] No se pudo encender web3: %v", err)
		} else {
			needWait = true
		}
	} else {
		log.Printf("[INFO] web3 ya está corriendo.")
	}

	if needWait {
		log.Printf("[INFO] Esperando que los servicios arranquen...")
		time.Sleep(30 * time.Second)
	}
}

func RecoverInstances() {
	log.Printf("[INFO] Recuperando instancias previas...")

	out, err := exec.Command("vboxmanage", "list", "vms").CombinedOutput()
	if err != nil {
		log.Printf("[WARN] No se pudo listar VMs: %v", err)
		return
	}

	natOut, _ := vbox("natnetwork", "list")

	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "\"apache-") {
			continue
		}
		parts := strings.SplitN(line, "\"", 3)
		if len(parts) < 2 {
			continue
		}
		vmName := parts[1]
		host := strings.TrimPrefix(vmName, "apache-")

		sshPort := 0
		httpPort := 0
		for _, natLine := range strings.Split(natOut, "\n") {
			natLine = strings.TrimSpace(natLine)
			if strings.HasPrefix(natLine, host+"-ssh:") {
				fields := strings.Split(natLine, ":")
				if len(fields) >= 4 {
					fmt.Sscanf(fields[3], "%d", &sshPort)
				}
			}
			if strings.HasPrefix(natLine, host+"-http:") {
				fields := strings.Split(natLine, ":")
				if len(fields) >= 4 {
					fmt.Sscanf(fields[3], "%d", &httpPort)
				}
			}
		}

		if sshPort == 0 || httpPort == 0 {
			log.Printf("[WARN] No se encontraron puertos para '%s', saltando", host)
			continue
		}

		runningOut, _ := exec.Command("vboxmanage", "list", "runningvms").CombinedOutput()
		state := "Inactivo"
		if strings.Contains(string(runningOut), "\""+vmName+"\"") {
			state = "Activo"
		}

		instances = append(instances, Instance{
			Host:      host,
			IP:        "192.168.10.x",
			URL:       fmt.Sprintf("http://%s.cloud.local", host),
			State:     state,
			CreatedAt: time.Now(),
			SSHPort:   sshPort,
			HTTPPort:  httpPort,
		})
		log.Printf("[INFO] Instancia recuperada: %s (SSH:%d HTTP:%d) [%s]", host, sshPort, httpPort, state)
	}

	log.Printf("[INFO] %d instancia(s) recuperadas.", len(instances))
}

func sshRun(host, port, cmd string) (string, error) {
	config := &ssh.ClientConfig{
		User:            SSH_USER,
		Auth:            []ssh.AuthMethod{ssh.Password(SSH_PASS)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}
	client, err := ssh.Dial("tcp", host+":"+port, config)
	if err != nil {
		return "", fmt.Errorf("error conectando SSH a %s:%s: %w", host, port, err)
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

func vbox(args ...string) (string, error) {
	cmd := exec.Command("vboxmanage", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	corsHeaders(w)
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func GetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		return
	}

	s := StatusInfo{}

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

	connDNS, errDNS := net.DialTimeout("tcp", "127.0.0.1:"+NS_DNS_PORT, 2*time.Second)
	if errDNS != nil {
		s.DNS = "ERROR"
		s.DNSDetail = "ns no responde en puerto " + NS_DNS_PORT
	} else {
		connDNS.Close()
		s.DNS = "OK"
		s.DNSDetail = "ns.cloud.local activo"
	}

	connWeb, errWeb := net.DialTimeout("tcp", "127.0.0.1:"+WEB3_HTTP_PORT, 2*time.Second)
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
	internalIP, sshPort, httpPort := nextPorts()
	mu.Unlock()

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
		addLog("INFO", fmt.Sprintf("Iniciando aprovisionamiento de '%s.cloud.local' (IP interna: %s)...", host, internalIP))
		mu.Unlock()

		if err := provisionVM(host, internalIP, sshPort, httpPort); err != nil {
			mu.Lock()
			addLog("ERROR", fmt.Sprintf("Error al crear VM '%s': %v", host, err))
			mu.Unlock()
			return
		}
		mu.Lock()
		addLog("INFO", fmt.Sprintf("Instancia '%s.cloud.local' aprovisionada con éxito.", host))
		mu.Unlock()

		if err := dnsAdd(host, internalIP); err != nil {
			mu.Lock()
			addLog("ERROR", fmt.Sprintf("Error al registrar DNS para '%s': %v", host, err))
			mu.Unlock()
			return
		}
		mu.Lock()
		addLog("INFO", fmt.Sprintf("Registro DNS '%s.cloud.local' → %s creado.", host, internalIP))
		mu.Unlock()

		if err := deployZip(host, sshPort, httpPort, zipPath); err != nil {
			mu.Lock()
			addLog("ERROR", fmt.Sprintf("Error al desplegar contenido en '%s': %v", host, err))
			mu.Unlock()
			return
		}

		mu.Lock()
		addLog("INFO", fmt.Sprintf("Instancia '%s.cloud.local' (IP: %s) publicada con éxito.", host, internalIP))
		instances = append(instances, Instance{
			Host:      host,
			IP:        internalIP + "/24",
			URL:       fmt.Sprintf("http://%s.cloud.local", host),
			State:     "Activo",
			CreatedAt: time.Now(),
			SSHPort:   sshPort,
			HTTPPort:  httpPort,
		})
		mu.Unlock()
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"message": fmt.Sprintf("Aprovisionamiento de '%s.cloud.local' iniciado.", host),
		"ip":      internalIP,
	})
}

func provisionVM(host, internalIP string, sshPort, httpPort int) error {
	vmName := "apache-" + host

	if _, err := vbox("createvm", "--name", vmName, "--ostype", "Debian_64", "--register"); err != nil {
		return fmt.Errorf("createvm: %w", err)
	}

	if _, err := vbox("modifyvm", vmName,
		"--memory", "512",
		"--cpus", "1",
		"--nic1", "intnet",
		"--intnet1", INTERNAL_NET,
		"--nic2", "natnetwork",
		"--nat-network2", NAT_NET,
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
		"--port", "0", "--device", "0",
		"--type", "hdd",
		"--medium", BASE_DISK_PATH,
		"--mtype", "multiattach",
	); err != nil {
		return fmt.Errorf("storageattach: %w", err)
	}

	out, _ := vbox("showvminfo", vmName, "--machinereadable")
	var mac string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "macaddress2=") {
			raw := strings.Trim(strings.Split(line, "=")[1], "\"")
			mac = fmt.Sprintf("%s:%s:%s:%s:%s:%s",
				raw[0:2], raw[2:4], raw[4:6], raw[6:8], raw[8:10], raw[10:12])
			break
		}
	}
	if mac == "" {
		return fmt.Errorf("no se pudo obtener MAC de enp0s8")
	}
	log.Printf("[INFO] MAC de enp0s8: %s", mac)

	log.Printf("[INFO] Apagando web3 temporalmente...")
	vbox("controlvm", "web3", "acpipowerbutton")
	time.Sleep(20 * time.Second)

	if _, err := vbox("startvm", vmName, "--type", "headless"); err != nil {
		return fmt.Errorf("startvm: %w", err)
	}

	log.Printf("[INFO] Esperando que '%s' arranque...", vmName)
	time.Sleep(60 * time.Second)

	var dhcpIP string
	for i := 0; i < 12; i++ {
		out, err := vbox("dhcpserver", "findlease", "--network", NAT_NET, "--mac-address", mac)
		if err == nil {
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, "IP Address:") {
					dhcpIP = strings.TrimSpace(strings.Split(line, ":")[1])
					break
				}
			}
		}
		if dhcpIP != "" {
			log.Printf("[INFO] IP DHCP asignada a nueva VM: %s", dhcpIP)
			break
		}
		log.Printf("[INFO] Esperando IP DHCP (%d/12)...", i+1)
		time.Sleep(10 * time.Second)
	}
	if dhcpIP == "" {
		return fmt.Errorf("no se pudo obtener IP DHCP de la nueva VM")
	}

	portName := fmt.Sprintf("%s-ssh", host)
	portNameHTTP := fmt.Sprintf("%s-http", host)
	out2, _ := vbox("natnetwork", "list")
	if !strings.Contains(out2, portName) {
		if _, err := vbox("natnetwork", "modify", "--netname", NAT_NET,
			fmt.Sprintf("--port-forward-4=%s:tcp:[]:%d:[%s]:22", portName, sshPort, dhcpIP),
		); err != nil {
			return fmt.Errorf("port-forward ssh: %w", err)
		}
	}
	if !strings.Contains(out2, portNameHTTP) {
		if _, err := vbox("natnetwork", "modify", "--netname", NAT_NET,
			fmt.Sprintf("--port-forward-4=%s:tcp:[]:%d:[%s]:80", portNameHTTP, httpPort, dhcpIP),
		); err != nil {
			return fmt.Errorf("port-forward http: %w", err)
		}
	}

	configCmd := fmt.Sprintf(`
sudo hostnamectl set-hostname %s.cloud.local
sudo sed -i 's/127.0.1.1.*/127.0.1.1\t%s.cloud.local/' /etc/hosts
sudo tee /etc/network/interfaces > /dev/null <<'EOF'
auto lo
iface lo inet loopback

auto enp0s3
iface enp0s3 inet static
    address %s
    netmask %s
    dns-nameservers %s

auto enp0s8
iface enp0s8 inet dhcp
EOF
sudo tee /etc/resolv.conf > /dev/null <<'EOF2'
nameserver %s
EOF2
nohup sudo systemctl restart networking > /dev/null 2>&1 &
nohup sudo systemctl restart apache2 > /dev/null 2>&1 &
`, host, host,
		internalIP, NETMASK_INTERNAL, DNS_INTERNAL,
		DNS_INTERNAL)

	var sshErr error
	for i := 0; i < 10; i++ {
		_, sshErr = sshRun("127.0.0.1", strconv.Itoa(sshPort), configCmd)
		if sshErr == nil {
			break
		}
		log.Printf("[INFO] Esperando SSH en nueva VM (%d/10)...", i+1)
		time.Sleep(10 * time.Second)
	}
	if sshErr != nil {
		return fmt.Errorf("ssh config: %w", sshErr)
	}

	log.Printf("[INFO] Encendiendo web3...")
	vbox("startvm", "web3", "--type", "headless")
	time.Sleep(10 * time.Second)

	return nil
}

func dnsAdd(host, ip string) error {
	cmd := fmt.Sprintf(`
SERIAL=$(grep -oP '\d+' %s | head -1)
NEW_SERIAL=$((SERIAL + 1))
sudo sed -i "s/$SERIAL/$NEW_SERIAL/" %s
if ! grep -q '^%s[[:space:]]' %s; then
    echo '%s    IN  A   %s' | sudo tee -a %s > /dev/null
fi
sudo rndc reload cloud.local
`, ZONE_FILE, ZONE_FILE, host, ZONE_FILE, host, ip, ZONE_FILE)

	_, err := sshRun("127.0.0.1", NS_SSH_PORT, cmd)
	return err
}

func dnsRemove(host string) error {
	cmd := fmt.Sprintf(`
SERIAL=$(grep -oP '\d+' %s | head -1)
NEW_SERIAL=$((SERIAL + 1))
sudo sed -i "s/$SERIAL/$NEW_SERIAL/" %s
sudo sed -i '/^%s[[:space:]]/d' %s
sudo rndc reload cloud.local
`, ZONE_FILE, ZONE_FILE, host, ZONE_FILE)

	_, err := sshRun("127.0.0.1", NS_SSH_PORT, cmd)
	return err
}

func deployZip(host string, sshPort, httpPort int, zipPath string) error {
	webRoot := "/var/www/html/" + host

	zipData, err := os.ReadFile(zipPath)
	if err != nil {
		return fmt.Errorf("error leyendo zip: %w", err)
	}

	port := strconv.Itoa(sshPort)

	log.Printf("[INFO] Esperando SSH en nueva instancia (puerto %s)...", port)
	for i := 0; i < 12; i++ {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 3*time.Second)
		if err == nil {
			conn.Close()
			break
		}
		log.Printf("[INFO] Esperando SSH (%d/12)...", i+1)
		time.Sleep(10 * time.Second)
	}

	config := &ssh.ClientConfig{
		User:            SSH_USER,
		Auth:            []ssh.AuthMethod{ssh.Password(SSH_PASS)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	client, err := ssh.Dial("tcp", "127.0.0.1:"+port, config)
	if err != nil {
		return fmt.Errorf("error SSH a nueva instancia: %w", err)
	}
	defer client.Close()

	log.Printf("[INFO] Copiando zip a nueva instancia...")
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("error creando sesión: %w", err)
	}
	stdin, _ := session.StdinPipe()
	session.Start("cat > /tmp/contenido.zip")
	stdin.Write(zipData)
	stdin.Close()
	session.Wait()
	session.Close()

	apacheConf := fmt.Sprintf(`<VirtualHost *:80>
    ServerName %s.cloud.local
    DocumentRoot %s
    <Directory %s>
        Options Indexes FollowSymLinks
        AllowOverride All
        Require all granted
    </Directory>
</VirtualHost>`, host, webRoot, webRoot)

	deployCmd := fmt.Sprintf(`
sudo mkdir -p %s &&
sudo unzip -o /tmp/contenido.zip -d %s &&
sudo chown -R www-data:www-data %s &&
sudo tee /etc/apache2/sites-available/%s.conf > /dev/null <<'APACHEEOF'
%s
APACHEEOF
sudo a2ensite %s.conf &&
sudo a2dissite 000-default.conf 2>/dev/null || true &&
sudo systemctl reload apache2
`, webRoot, webRoot, webRoot, host, apacheConf, host)

	session2, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("error creando sesión deploy: %w", err)
	}
	out, err := session2.CombinedOutput(deployCmd)
	session2.Close()
	if err != nil {
		return fmt.Errorf("error en deploy: %s: %w", string(out), err)
	}

	// Agregar entrada en hosts de Windows
	hostsEntry := fmt.Sprintf("127.0.0.1\t%s.cloud.local\n", host)
	hostsPath := `C:\Windows\System32\drivers\etc\hosts`
	f, err := os.OpenFile(hostsPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[WARN] No se pudo actualizar hosts de Windows: %v", err)
	} else {
		f.WriteString(hostsEntry)
		f.Close()
		log.Printf("[INFO] Entrada agregada en hosts: %s.cloud.local → 127.0.0.1", host)
	}

	log.Printf("[INFO] Contenido desplegado en http://%s.cloud.local", host)
	return nil
}

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
			vbox("controlvm", "apache-"+inst.Host, "poweroff")
			time.Sleep(2 * time.Second)
			vbox("unregistervm", "apache-"+inst.Host, "--delete")

			vbox("natnetwork", "modify", "--netname", NAT_NET,
				fmt.Sprintf("--port-forward-4=delete:%s-ssh", inst.Host))
			vbox("natnetwork", "modify", "--netname", NAT_NET,
				fmt.Sprintf("--port-forward-4=delete:%s-http", inst.Host))

			// Eliminar entrada del hosts de Windows
			hostsPath := `C:\Windows\System32\drivers\etc\hosts`
			data, err := os.ReadFile(hostsPath)
			if err == nil {
				lines := strings.Split(string(data), "\n")
				var newLines []string
				for _, line := range lines {
					if !strings.Contains(line, inst.Host+".cloud.local") {
						newLines = append(newLines, line)
					}
				}
				os.WriteFile(hostsPath, []byte(strings.Join(newLines, "\n")), 0644)
				log.Printf("[INFO] Entrada eliminada del hosts: %s.cloud.local", inst.Host)
			}

			if err := dnsRemove(inst.Host); err != nil {
				addLog("ERROR", fmt.Sprintf("Error eliminando DNS de '%s': %v", inst.Host, err))
			}

			instances = append(instances[:i], instances[i+1:]...)
			addLog("INFO", fmt.Sprintf("Instancia '%s.cloud.local' eliminada con éxito.", inst.Host))
			writeJSON(w, http.StatusOK, map[string]string{"message": "instancia eliminada"})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "instancia no encontrada"})
}

func GetInstancesList() []Instance {
	mu.Lock()
	defer mu.Unlock()
	return instances
}
