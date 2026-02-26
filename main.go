package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

type TelemetryPayload struct {
	DeviceID  string  `json:"device_id"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timestamp int64   `json:"timestamp"`
}

var rdb *redis.Client
var db *sql.DB
var ctx = context.Background()

// --- PROMETHEUS METRICS ---
var (
	payloadsIngested = promauto.NewCounter(prometheus.CounterOpts{
		Name: "geo_payloads_ingested_total",
		Help: "The total number of payloads received by the API",
	})
	payloadsProcessed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "geo_payloads_processed_total",
		Help: "The total number of payloads saved to the database",
	})
)

func initRedis() {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	rdb = redis.NewClient(&redis.Options{Addr: redisAddr})
}

func initDB() {
	connStr := "postgres://niantic_user:niantic_pass@localhost:5432/spatial_db?sslmode=disable"
	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Fatalf("Failed to connect to DB: %v", err)
	}

	_, _ = db.Exec("CREATE EXTENSION IF NOT EXISTS postgis;")

	createTableQuery := `
	CREATE TABLE IF NOT EXISTS telemetry (
		id SERIAL PRIMARY KEY,
		device_id VARCHAR(255),
		location GEOMETRY(Point, 4326),
		timestamp BIGINT
	);`
	_, err = db.Exec(createTableQuery)
	if err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}
}

func startWorker() {
	fmt.Println("Background worker started, listening to queue...")
	for {
		result, err := rdb.BRPop(ctx, 0, "telemetry_queue").Result()
		if err != nil {
			continue
		}

		var payload TelemetryPayload
		err = json.Unmarshal([]byte(result[1]), &payload)
		if err != nil {
			continue
		}

		insertQuery := `
			INSERT INTO telemetry (device_id, location, timestamp) 
			VALUES ($1, ST_SetSRID(ST_MakePoint($2, $3), 4326), $4)
		`
		_, err = db.Exec(insertQuery, payload.DeviceID, payload.Longitude, payload.Latitude, payload.Timestamp)
		if err == nil {
			fmt.Printf("Saved telemetry for device %s to database\n", payload.DeviceID)
			// Increment the processed metric!
			payloadsProcessed.Inc()
		}
	}
}

func ingestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST method is allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload TelemetryPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	data, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "Failed to serialize data", http.StatusInternalServerError)
		return
	}

	if err = rdb.LPush(ctx, "telemetry_queue", data).Err(); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Increment the ingested metric!
	payloadsIngested.Inc()

	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintf(w, "Payload ingested and queued successfully\n")
}

func main() {
	initRedis()
	initDB()
	go startWorker()

	http.HandleFunc("/ingest", ingestHandler)

	// Expose the /metrics endpoint for Prometheus
	http.Handle("/metrics", promhttp.Handler())
go
	port := "8080"
	fmt.Printf("Ingestion API running on port %s...\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
