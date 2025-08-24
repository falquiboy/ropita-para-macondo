package main

import (
    "log"
    "net/http"
    "os"
    "time"

    "dupman/backend/internal/api"
    "dupman/backend/internal/engine"
)

func main() {
    port := os.Getenv("PORT")
    if port == "" {
        port = "8080"
    }
    addr := ":" + port

    // Select engine implementation
    var eng api.Engine
    switch os.Getenv("ENGINE") {
    case "macondo":
        mac, err := engine.NewMacondoEngineFromEnv()
        if err != nil {
            log.Fatalf("failed to init Macondo engine: %v", err)
        }
        eng = mac
        log.Printf("Using engine: macondo (bin=%s)", mac.BinPath)
    default:
        eng = engine.NewStubEngine()
        log.Printf("Using engine: stub")
    }

    srv := &http.Server{
        Addr:              addr,
        Handler:           api.Router(eng),
        ReadHeaderTimeout: 5 * time.Second,
    }
    log.Printf("Duplicate Tournament Manager listening on %s", addr)
    log.Fatal(srv.ListenAndServe())
}
