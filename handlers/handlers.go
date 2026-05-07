package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ─── Models ───────────────────────────────────────────────────────────────────

type Instance struct {
	Host      string    `json:"host"`
	IP        string    `json:"ip"`
	URL       string    `json:"url"`
	State     string    `json:"state"` // "Activo" | "Inactivo"
	CreatedAt time.Time `json:"created_at"`
}

type StatusInfo struct {
	VirtualBox string `json:"virtualbox"` // "OK" | "ERROR"
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

// IPs para nuevas instancias Apache: empieza en .32 (web1=.30 es la plantilla)
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

// ─── Helpers ──────────────────────────────────────────────────────────────────

func runScript(name string, args ...string) (string, error) {
	scriptPath := filepath.Join("scripts", name)
	cmd := exec.Command("bash", append([]string{scriptPath}, args...)...)
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

	// DNS bind9 — check port 53 on ns1
	out2, err2 := exec.Command("bash", "-c",
		"nc -z -w2 192.168.10.10 53 && echo OK || echo FAIL").CombinedOutput()
	if err2 != nil || strings.TrimSpace(string(out2)) != "OK" {
		s.DNS = "ERROR"
		s.DNSDetail = "ns1 (192.168.10.10) no responde en puerto 53"
	} else {
		s.DNS = "OK"
		s.DNSDetail = "ns1.cloud.local activo"
	}

	// Apache plantilla — check web1
	out3, err3 := exec.Command("bash", "-c",
		"nc -z -w2 192.168.10.30 80 && echo OK || echo FAIL").CombinedOutput()
	if err3 != nil || strings.TrimSpace(string(out3)) != "OK" {
		s.Apache = "ERROR"
		s.ApacheMsg = "Plantilla base no encontrada"
	} else {
		s.Apache = "OK"
		s.ApacheMsg = "web1.cloud.local activo"
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

	resp := map[string]any{
		"instances": instances,
		"logs":      logs,
	}
	writeJSON(w, http.StatusOK, resp)
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

	// Parse multipart (max 50 MB)
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "error al parsear formulario"})
		return
	}

	host := strings.TrimSpace(r.FormValue("host"))
	if host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nombre de host requerido"})
		return
	}
	// Sanitize host
	host = strings.ToLower(host)
	host = strings.ReplaceAll(host, " ", "-")

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
	// Check duplicate
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

	// Save zip to /tmp
	zipPath := filepath.Join("/tmp", fh.Filename)
	dst, err := os.Create(zipPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no se pudo guardar el zip"})
		return
	}
	io.Copy(dst, file)
	dst.Close()

	// Run provisioning in background
	go func() {
		mu.Lock()
		addLog("INFO", fmt.Sprintf("Iniciando aprovisionamiento de '%s.cloud.local' (IP: %s)...", host, ip))
		mu.Unlock()

		// 1. Create VM
		out, err := runScript("provision.sh", host, ip)
		mu.Lock()
		if err != nil {
			addLog("ERROR", fmt.Sprintf("Error al crear VM '%s': %s", host, strings.TrimSpace(out)))
			mu.Unlock()
			return
		}
		addLog("INFO", fmt.Sprintf("Instancia '%s.cloud.local' (IP: %s) aprovisionada con éxito.", host, ip))
		mu.Unlock()

		// 2. Register DNS
		out, err = runScript("dns_add.sh", host, ip)
		mu.Lock()
		if err != nil {
			addLog("ERROR", fmt.Sprintf("Error al registrar DNS para '%s': %s", host, strings.TrimSpace(out)))
			mu.Unlock()
			return
		}
		addLog("INFO", fmt.Sprintf("Registro DNS '%s.cloud.local' → %s creado.", host, ip))
		mu.Unlock()

		// 3. Deploy zip
		out, err = runScript("deploy.sh", host, ip, zipPath)
		mu.Lock()
		if err != nil {
			addLog("ERROR", fmt.Sprintf("Error al desplegar contenido en '%s': %s", host, strings.TrimSpace(out)))
			mu.Unlock()
			return
		}
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

// ─── POST /api/instances/start ────────────────────────────────────────────────

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
			out, err := runScript("vm_power.sh", inst.Host, "start")
			if err != nil {
				addLog("ERROR", fmt.Sprintf("Error al encender '%s': %s", inst.Host, strings.TrimSpace(out)))
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

// ─── POST /api/instances/stop ─────────────────────────────────────────────────

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
			out, err := runScript("vm_power.sh", inst.Host, "stop")
			if err != nil {
				addLog("ERROR", fmt.Sprintf("Error al apagar '%s': %s", inst.Host, strings.TrimSpace(out)))
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

// ─── DELETE /api/instances/delete ─────────────────────────────────────────────

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
			// Remove VM
			out, err := runScript("provision.sh", inst.Host, inst.IP, "delete")
			if err != nil {
				addLog("ERROR", fmt.Sprintf("Error al eliminar VM '%s': %s", inst.Host, strings.TrimSpace(out)))
			}
			// Remove DNS
			runScript("dns_remove.sh", inst.Host)

			instances = append(instances[:i], instances[i+1:]...)
			addLog("INFO", fmt.Sprintf("Instancia '%s.cloud.local' (IP: %s) eliminada con éxito.", inst.Host, inst.IP))
			writeJSON(w, http.StatusOK, map[string]string{"message": "instancia eliminada"})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "instancia no encontrada"})
}
