package main

import (
	"log/slog"
	"os"

	"github.com/incrementventures/govr/scan"
	"github.com/lmittmann/tint"
	"github.com/nyaruka/ezconf"
)

type Config struct {
	Port     int        `help:"the port to use when connecting to cameras"`
	Username string     `help:"the username to use when connecting to cameras (optional)"`
	Password string     `help:"the password to use when connecting to cameras (optional)"`
	Level    slog.Level `help:"the log level to use (optional)"`
}

func main() {
	config := &Config{
		Port:  80,
		Level: slog.LevelInfo,
	}
	// create our loader object, configured with configuration struct (must be a pointer), our name
	// and description, as well as any files we want to search for
	loader := ezconf.NewLoader(
		config,
		"govr-scan", "govr-scan - Scan for ONVIF cameras on local network",
		[]string{},
	)
	loader.MustLoad()

	log := slog.New(tint.NewHandler(os.Stderr, &tint.Options{Level: config.Level}))

	_, err := scan.GetDevicesOnNetwork(log, config.Port, config.Username, config.Password)
	if err != nil {
		panic(err)
	}
}
