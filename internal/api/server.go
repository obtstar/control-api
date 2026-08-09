// Package api HTTP 服务（stdlib，最小面）
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"control-api/internal/config"
	"control-api/internal/store"
)

func Serve(cfg *config.Config) error {
	st, err := store.Open(cfg.DB.Path)
	if err != nil {
		return err
	}
	defer st.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /actuator/health", func(w http.ResponseWriter, r *http.Request) {
		status := "UP"
		if err := st.Ping(); err != nil {
			status = "DOWN"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": status, "version": "0.1.0-dev"})
	})

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("control-api listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}
