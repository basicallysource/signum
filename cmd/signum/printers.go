package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/basicallysource/signum/internal/printwatch"
	"github.com/basicallysource/signum/internal/printwatch/driver"
)

// The printers a watcher tends are declared in one JSON file, added by hand
// for now -- discovery can come later without changing the shape:
//
//	[
//	  {"name": "p1s", "driver": "bambu", "host": "192.168.1.20",
//	   "serial": "01S00A000000000", "access_code": "12345678"},
//	  {"name": "fake", "driver": "mock", "path": "jobs.json"}
//	]
type printerConfig struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Host       string `json:"host"`
	Serial     string `json:"serial"`
	AccessCode string `json:"access_code"`
	Path       string `json:"path"`
}

// loadPrinters turns the file into drivers. No file is no printers.
func loadPrinters(path string, logger *slog.Logger) ([]printwatch.Driver, error) {
	if path == "" {
		return nil, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read printers file: %w", err)
	}
	var configs []printerConfig
	if err := json.Unmarshal(body, &configs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	var drivers []printwatch.Driver
	for _, config := range configs {
		if config.Name == "" {
			return nil, fmt.Errorf("%s: every printer needs a name", path)
		}
		switch config.Driver {
		case "bambu":
			if config.Host == "" || config.Serial == "" || config.AccessCode == "" {
				return nil, fmt.Errorf("%s: bambu printer %q needs host, serial, and access_code", path, config.Name)
			}
			drivers = append(drivers, &driver.Bambu{
				PrinterName: config.Name,
				Host:        config.Host,
				Serial:      config.Serial,
				AccessCode:  config.AccessCode,
				Logger:      logger,
			})
		case "mock":
			drivers = append(drivers, &driver.Mock{PrinterName: config.Name, Path: config.Path})
		default:
			return nil, fmt.Errorf("%s: printer %q has unknown driver %q", path, config.Name, config.Driver)
		}
	}
	return drivers, nil
}
