package instrument

import (
	"os"

	"github.com/seatgeek/telemetria"
	log "github.com/sirupsen/logrus"
)

// DefaultConfig Returns the InstrumentsCofig that is most suitable
// to the current environment given the INFLUXDB_URL, NEW_RELIC_NAME
// and NEW_RELIC_LICENSE env variables
func DefaultConfig(appName string) *InstrumentsConfig {
	influxDb := os.Getenv("INFLUXDB_URL")

	if len(influxDb) == 0 {
		influxDb = "udp://localhost:8090"
	}

	recorder, err := telemetria.NewRecorder(influxDb)

	if err != nil {
		log.Fatalf("Could not correctly configure influxdb connection: %s", err)
		panic(err)
	}

	return &InstrumentsConfig{
		statsRecorder: recorder,
	}
}

func checkBool(str string) bool {
	if str == "" {
		return false
	}
	if str == "0" {
		return false
	}
	if str == "false" {
		return false
	}
	if str == "1" {
		return true
	}
	if str == "true" {
		return true
	}

	return false
}
