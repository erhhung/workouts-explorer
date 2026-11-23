package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/osmcontroller"
)

func main() {
	if err := run(); err != nil {
		slog.Error("OSM controller stopped", "code", "controller-unavailable")
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	concurrency := 1
	var err error
	if raw := os.Getenv("OSM_UPDATE_CONCURRENCY"); raw != "" {
		concurrency, err = strconv.Atoi(raw)
	}
	if err != nil || concurrency < 1 || concurrency > 8 {
		return errors.New("invalid update concurrency")
	}
	app, err := database.OpenWithMaxConns(ctx, os.Getenv("OSM_CONTROLLER_DATABASE_URL"), "workouts-osm-controller", 4)
	if err != nil {
		return err
	}
	defer app.Close()
	catalog, err := database.OpenWithMaxConns(ctx, os.Getenv("OSM_CATALOG_DATABASE_URL"), "workouts-osm-controller-catalog", 3)
	if err != nil {
		return err
	}
	defer catalog.Close()
	raw, err := os.ReadFile(os.Getenv("OSM_CONTROLLER_TEMPLATE_PATH"))
	if err != nil {
		return err
	}
	var template map[string]any
	if err = json.Unmarshal(raw, &template); err != nil {
		return err
	}
	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		return errors.New("controller namespace is unavailable")
	}
	jobs, err := osmcontroller.InCluster(namespace)
	if err != nil {
		return err
	}
	if err = jobs.DeploymentOwner(ctx, os.Getenv("OSM_CONTROLLER_DEPLOYMENT")); err != nil {
		return err
	}
	controller := &osmcontroller.Controller{DB: app, Catalog: catalog, Jobs: jobs, Template: template, Concurrency: concurrency, Logger: slog.Default()}
	address := os.Getenv("OSM_CONTROLLER_LISTEN_ADDRESS")
	if address == "" {
		address = ":8082"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		check, end := context.WithTimeout(r.Context(), 2*time.Second)
		defer end()
		if app.Ping(check) != nil || catalog.Ping(check) != nil {
			http.Error(w, "dependency unavailable", 503)
			return
		}
		w.WriteHeader(200)
	})
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	err = controller.Run(ctx)
	stop()
	shutdown, end := context.WithTimeout(context.Background(), 10*time.Second)
	defer end()
	_ = server.Shutdown(shutdown)
	return err
}
