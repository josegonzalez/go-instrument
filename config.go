package instrument

import (
	log "github.com/Sirupsen/logrus"
	newrelic "github.com/newrelic/go-agent"
	"github.com/newrelic/go-agent/_integrations/nrlogrus"
	"github.com/seatgeek/telemetria"
	"os"
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

	nr := setupNewRelic(appName)

	return &InstrumentsConfig{
		StatsRecorder: recorder,
		Tracer:        HasNewRelic{Application: &nr},
	}
}

func setupNewRelic(appName string) newrelic.Application {
	newrelicKey := os.Getenv("NEW_RELIC_LICENSE")
	newrelicName := os.Getenv("NEW_RELIC_NAME")
	shouldDisable := os.Getenv("NEW_RELIC_DISABLE")

	if newrelicName == "" {
		newrelicName = appName
	}

	config := newrelic.NewConfig(newrelicName, newrelicKey)

	if checkBool(shouldDisable) {
		config.Enabled = false
	}

	config.Logger = nrlogrus.StandardLogger()
	app, err := newrelic.NewApplication(config)

	if err != nil {
		log.Fatalf("Invalid New Relic app name or license key: %s", err)
		panic(err)
	}

	return app
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
