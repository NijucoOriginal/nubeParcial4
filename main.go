package main

import (
	"log"
	"net/http"

	"compunube/handlers"
)

func main() {
	handlers.EnsureInfrastructure()
	handlers.LoadIPCounter()
	mux := http.NewServeMux()

	// Static frontend
	mux.Handle("/", http.FileServer(http.Dir("./static")))

	// API endpoints
	mux.HandleFunc("/api/status", handlers.GetStatus)
	mux.HandleFunc("/api/instances", handlers.GetInstances)
	mux.HandleFunc("/api/provision", handlers.Provision)
	mux.HandleFunc("/api/instances/start", handlers.StartInstance)
	mux.HandleFunc("/api/instances/stop", handlers.StopInstance)
	mux.HandleFunc("/api/instances/delete", handlers.DeleteInstance)

	log.Println("Servidor iniciado en http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
