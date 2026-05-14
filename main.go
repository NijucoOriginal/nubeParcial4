package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"compunube/handlers"
)

func main() {
	handlers.EnsureInfrastructure()
	handlers.LoadIPCounter()
	handlers.RecoverInstances()

	// ── Mux principal (puerto 8080) ──────────────────────────────────────────
	mux := http.NewServeMux()
	mux.Handle("/static/", http.FileServer(http.Dir(".")))
	mux.HandleFunc("/api/status", handlers.GetStatus)
	mux.HandleFunc("/api/instances", handlers.GetInstances)
	mux.HandleFunc("/api/provision", handlers.Provision)
	mux.HandleFunc("/api/instances/start", handlers.StartInstance)
	mux.HandleFunc("/api/instances/stop", handlers.StopInstance)
	mux.HandleFunc("/api/instances/delete", handlers.DeleteInstance)
	mux.Handle("/", http.FileServer(http.Dir("./static")))

	// ── Proxy inverso (puerto 80) ────────────────────────────────────────────
	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extraer host sin puerto
		hostHeader := r.Host
		if idx := strings.Index(hostHeader, ":"); idx != -1 {
			hostHeader = hostHeader[:idx]
		}

		// Verificar si es un dominio .cloud.local
		if strings.HasSuffix(hostHeader, ".cloud.local") {
			instanceHost := strings.TrimSuffix(hostHeader, ".cloud.local")

			// Buscar la instancia
			for _, inst := range handlers.GetInstancesList() {
				if inst.Host == instanceHost {
					// Hacer proxy hacia el puerto HTTP de la instancia
					target := fmt.Sprintf("http://127.0.0.1:%d%s", inst.HTTPPort, r.URL.RequestURI())
					resp, err := http.Get(target)
					if err != nil {
						http.Error(w, "Error conectando con la instancia", http.StatusBadGateway)
						return
					}
					defer resp.Body.Close()

					// Copiar headers
					for k, v := range resp.Header {
						for _, vv := range v {
							w.Header().Add(k, vv)
						}
					}
					w.WriteHeader(resp.StatusCode)
					io.Copy(w, resp.Body)
					return
				}
			}
			http.Error(w, "Instancia no encontrada", http.StatusNotFound)
			return
		}

		// Si no es .cloud.local servir el frontend normal
		http.FileServer(http.Dir("./static")).ServeHTTP(w, r)
	})

	// Arrancar proxy en puerto 80 en segundo plano
	go func() {
		log.Println("Proxy inverso iniciado en puerto 80")
		if err := http.ListenAndServe(":80", proxy); err != nil {
			log.Printf("[WARN] No se pudo iniciar proxy en puerto 80: %v", err)
			log.Println("Ejecuta la aplicación como administrador para usar el puerto 80")
		}
	}()

	// Arrancar servidor principal en puerto 8080
	log.Println("Servidor iniciado en http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
