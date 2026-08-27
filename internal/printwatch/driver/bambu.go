package driver

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/basicallysource/signum/internal/printwatch"
)

// Bambu watches one Bambu Lab printer over its LAN-mode MQTT: username is
// always "bblp", the password is the access code off the printer's own
// screen, and the certificate is self-signed, so the access code is the
// entire trust story -- which is fine on the LAN this only ever runs on.
//
// The connection is held open across polls; the printer pushes partial
// state, a full snapshot is requested on every (re)connect, and Poll just
// reads the machine this feeds.
type Bambu struct {
	PrinterName string
	Host        string
	Serial      string
	AccessCode  string
	Logger      *slog.Logger

	mu      sync.Mutex
	machine *bambuMachine
	client  mqtt.Client
}

const (
	bambuMQTTPort       = 8883
	bambuConnectTimeout = 10 * time.Second
)

// Name identifies the printer.
func (b *Bambu) Name() string { return b.PrinterName }

// Poll returns the machine's current snapshot, connecting on first use. A
// printer that is off is an error each tick, which the watcher logs and
// retries; the paho client keeps reconnecting on its own once it has been
// started.
func (b *Bambu) Poll(ctx context.Context) ([]printwatch.Job, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.machine == nil {
		b.machine = newBambuMachine(b.PrinterName)
	}
	if b.client == nil {
		b.connect()
	}
	if !b.client.IsConnectionOpen() {
		return nil, fmt.Errorf("driver: %s (%s) is not answering", b.PrinterName, b.Host)
	}
	return b.machine.Jobs(), nil
}

// connect starts the long-lived client. Called under b.mu.
func (b *Bambu) connect() {
	report := fmt.Sprintf("device/%s/report", b.Serial)
	request := fmt.Sprintf("device/%s/request", b.Serial)
	pushAll := []byte(`{"pushing":{"sequence_id":"signum","command":"pushall"}}`)

	options := mqtt.NewClientOptions().
		AddBroker(fmt.Sprintf("tls://%s:%d", b.Host, bambuMQTTPort)).
		SetClientID("signum-" + tail(b.Serial, 8)).
		SetUsername("bblp").
		SetPassword(b.AccessCode).
		// The printer's certificate is self-signed; the access code in the
		// CONNECT is what authenticates this conversation.
		SetTLSConfig(&tls.Config{InsecureSkipVerify: true}).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(15 * time.Second).
		SetConnectTimeout(bambuConnectTimeout).
		SetKeepAlive(30 * time.Second).
		SetOrderMatters(false)

	options.OnConnect = func(client mqtt.Client) {
		b.logger().Info("printer connected", "printer", b.PrinterName, "host", b.Host)
		client.Subscribe(report, 0, func(_ mqtt.Client, message mqtt.Message) {
			b.mu.Lock()
			b.machine.Apply(message.Payload(), time.Now().UTC())
			b.mu.Unlock()
		})
		// Ask for the full picture; everything after arrives as deltas.
		client.Publish(request, 0, false, pushAll)
	}
	options.OnConnectionLost = func(_ mqtt.Client, err error) {
		b.logger().Warn("printer connection lost", "printer", b.PrinterName, "error", err)
	}

	b.client = mqtt.NewClient(options)
	b.client.Connect()
}

func (b *Bambu) logger() *slog.Logger {
	if b.Logger != nil {
		return b.Logger
	}
	return slog.Default()
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
