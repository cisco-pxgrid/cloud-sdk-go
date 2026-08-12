package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	sdk "github.com/cisco-pxgrid/cloud-sdk-go"
	"github.com/cisco-pxgrid/cloud-sdk-go/log"
	"gopkg.in/yaml.v2"
)

var logger = &log.DefaultLogger{Level: log.LogLevelInfo}

type appConfig struct {
	ID            string   `yaml:"id"`
	APIKey        string   `yaml:"apiKey"`
	GlobalFQDN    string   `yaml:"globalFQDN"`
	RegionalFQDN  string   `yaml:"regionalFQDN"`
	RegionalFQDNs []string `yaml:"regionalFQDNs"`
	ReadStream    string   `yaml:"readStream"`
	WriteStream   string   `yaml:"writeStream"`
	GroupID       string   `yaml:"groupId"`
}

type appInstanceConfig struct {
	OTP    string       `yaml:"otp"`
	Name   string       `yaml:"name"`
	ID     string       `yaml:"id"`
	APIKey string       `yaml:"apiKey"`
	Tenant tenantConfig `yaml:"tenant"`
}

type tenantConfig struct {
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	Token string `yaml:"token"`
}

type config struct {
	App         appConfig         `yaml:"app"`
	AppInstance appInstanceConfig `yaml:"appInstance"`
}

func loadConfig(file string) (*config, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var cfg config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *config) store(file string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0600)
}

type deviceRegistry struct {
	mu      sync.RWMutex
	devices map[string]*sdk.Device
}

func newDeviceRegistry() *deviceRegistry {
	return &deviceRegistry{devices: make(map[string]*sdk.Device)}
}

func (r *deviceRegistry) add(device *sdk.Device) {
	r.mu.Lock()
	r.devices[device.ID()] = device
	r.mu.Unlock()
}

func (r *deviceRegistry) remove(device *sdk.Device) {
	r.mu.Lock()
	delete(r.devices, device.ID())
	r.mu.Unlock()
}

func (r *deviceRegistry) snapshot() []*sdk.Device {
	r.mu.RLock()
	defer r.mu.RUnlock()
	devices := make([]*sdk.Device, 0, len(r.devices))
	for _, device := range r.devices {
		devices = append(devices, device)
	}
	return devices
}

type callbackSimulator struct {
	mode          string
	slowDuration  time.Duration
	blockDuration time.Duration
	done          <-chan struct{}
	count         uint64
}

func (s *callbackSimulator) handle(messageID string, device *sdk.Device, stream string, payload []byte) {
	n := atomic.AddUint64(&s.count, 1)
	action := s.mode
	if action == "cycle" {
		switch n % 3 {
		case 1:
			action = "normal"
		case 2:
			action = "slow"
		default:
			action = "blocked"
		}
	}

	logger.Infof("DIAGNOSTIC TEST callback started. sequence=%d action=%s msgID=%s tenant=%s device=%s topic=%s bytes=%d",
		n, action, messageID, device.Tenant().Name(), device.Name(), stream, len(payload))

	switch action {
	case "slow":
		s.wait("slow", s.slowDuration, n)
	case "blocked":
		s.wait("blocked", s.blockDuration, n)
	}

	logger.Infof("DIAGNOSTIC TEST callback returned. sequence=%d action=%s msgID=%s", n, action, messageID)
}

func (s *callbackSimulator) wait(action string, duration time.Duration, sequence uint64) {
	logger.Warnf("DIAGNOSTIC TEST simulating %s application callback. sequence=%d duration=%s", action, sequence, duration)
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-s.done:
		logger.Infof("DIAGNOSTIC TEST callback released by shutdown. sequence=%d action=%s", sequence, action)
	}
}

func publishEcho(ctx context.Context, device *sdk.Device, sequence uint64) error {
	payload, err := json.Marshal(map[string]interface{}{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"message":   "read-stream diagnostics verification",
		"sequence":  sequence,
		"device":    device.Name(),
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/pxgrid/echo/publish", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := device.Query(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("echo publish returned %s", resp.Status)
	}
	return nil
}

func runPublisher(ctx context.Context, registry *deviceRegistry, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var sequence uint64
	publish := func() {
		devices := registry.snapshot()
		if len(devices) == 0 {
			logger.Warnf("DIAGNOSTIC TEST cannot publish echo message: no active devices")
			return
		}
		for _, device := range devices {
			sequence++
			if err := publishEcho(ctx, device, sequence); err != nil {
				if ctx.Err() != nil {
					return
				}
				logger.Errorf("DIAGNOSTIC TEST echo publish failed. sequence=%d device=%s error=%v", sequence, device.Name(), err)
				continue
			}
			logger.Infof("DIAGNOSTIC TEST echo published. sequence=%d device=%s", sequence, device.Name())
		}
	}

	publish()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			publish()
		}
	}
}

func validateCallbackMode(mode string) error {
	switch mode {
	case "cycle", "normal", "slow", "blocked":
		return nil
	default:
		return fmt.Errorf("invalid callback mode %q; use cycle, normal, slow, or blocked", mode)
	}
}

func main() {
	configFile := flag.String("config", "", "Configuration YAML file to use (required)")
	debug := flag.Bool("debug", false, "Enable debug output")
	insecure := flag.Bool("insecure", false, "Skip TLS certificate verification")
	statusInterval := flag.Duration("status-interval", 10*time.Second, "Explicit read-stream status/summary interval")
	logEachMessage := flag.Bool("log-each-message", true, "Explicitly enable per-message arrival logging")
	publishInterval := flag.Duration("publish-interval", 3*time.Second, "Echo-message publish interval")
	callbackMode := flag.String("callback-mode", "cycle", "Callback behavior: cycle, normal, slow, or blocked")
	slowDuration := flag.Duration("slow-duration", 12*time.Second, "Duration of a simulated slow callback")
	blockDuration := flag.Duration("block-duration", 40*time.Second, "Duration of a simulated blocked callback")
	flag.Parse()

	if strings.TrimSpace(*configFile) == "" {
		logger.Errorf("-config is required")
		flag.Usage()
		os.Exit(2)
	}
	if *statusInterval <= 0 || *publishInterval <= 0 || *slowDuration <= 0 || *blockDuration <= 0 {
		logger.Errorf("all duration flags must be greater than zero")
		os.Exit(2)
	}
	if err := validateCallbackMode(*callbackMode); err != nil {
		logger.Errorf("%v", err)
		os.Exit(2)
	}

	cfg, err := loadConfig(*configFile)
	if err != nil {
		panic(err)
	}

	log.Logger = logger
	if *debug {
		logger.Level = log.LogLevelDebug
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	registry := newDeviceRegistry()
	simulator := &callbackSimulator{
		mode:          *callbackMode,
		slowDuration:  *slowDuration,
		blockDuration: *blockDuration,
		done:          ctx.Done(),
	}

	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: *insecure,
		},
	}

	appConfig := sdk.Config{
		ID: cfg.App.ID,
		GetCredentials: func() (*sdk.Credentials, error) {
			return &sdk.Credentials{ApiKey: []byte(cfg.App.APIKey)}, nil
		},
		GlobalFQDN:    cfg.App.GlobalFQDN,
		RegionalFQDN:  cfg.App.RegionalFQDN,
		RegionalFQDNs: cfg.App.RegionalFQDNs,
		ReadStreamID:  cfg.App.ReadStream,
		WriteStreamID: cfg.App.WriteStream,
		GroupID:       cfg.App.GroupID,
		Transport:     transport,

		// Set every diagnostic option explicitly. The SDK defaults are intentionally not used.
		StatusLogInterval: *statusInterval,
		LogEachMessage:    logEachMessage,

		DeviceMessageHandler: simulator.handle,
		DeviceActivationHandler: func(device *sdk.Device) {
			registry.add(device)
			logger.Infof("Device activated. id=%s name=%s", device.ID(), device.Name())
		},
		DeviceDeactivationHandler: func(device *sdk.Device) {
			registry.remove(device)
			logger.Infof("Device deactivated. id=%s name=%s", device.ID(), device.Name())
		},
		TenantUnlinkedHandler: func(tenant *sdk.Tenant) {
			logger.Infof("Tenant unlinked. id=%s name=%s", tenant.ID(), tenant.Name())
		},
	}

	logger.Infof("DIAGNOSTIC TEST configuration. statusInterval=%s logEachMessage=%t publishInterval=%s callbackMode=%s slowDuration=%s blockDuration=%s",
		*statusInterval, *logEachMessage, *publishInterval, *callbackMode, *slowDuration, *blockDuration)

	app, err := sdk.New(appConfig)
	if err != nil {
		panic(err)
	}
	defer app.Close()

	instanceCfg := &cfg.AppInstance
	var appInstance *sdk.App
	var tenant *sdk.Tenant
	if instanceCfg.OTP != "" {
		appInstance, err = app.CreateAppInstance(instanceCfg.Name)
		if err == nil {
			tenant, err = appInstance.LinkTenant(instanceCfg.OTP)
		}
		if err == nil {
			instanceCfg.OTP = ""
			instanceCfg.ID = appInstance.ID()
			instanceCfg.APIKey = appInstance.ApiKey()
			instanceCfg.Tenant.ID = tenant.ID()
			instanceCfg.Tenant.Name = tenant.Name()
			instanceCfg.Tenant.Token = tenant.ApiToken()
			err = cfg.store(*configFile)
		}
	} else {
		appInstance, err = app.SetAppInstance(instanceCfg.ID, instanceCfg.APIKey)
		if err == nil {
			tenant, err = appInstance.SetTenant(instanceCfg.Tenant.ID, instanceCfg.Tenant.Name, instanceCfg.Tenant.Token)
		}
	}
	if err != nil {
		panic(err)
	}
	defer appInstance.Close()

	devices, err := tenant.GetDevices()
	if err != nil {
		panic(err)
	}
	for i := range devices {
		registry.add(&devices[i])
		logger.Infof("Active device loaded. id=%s name=%s region=%s", devices[i].ID(), devices[i].Name(), devices[i].Region())
	}
	logger.Infof("DIAGNOSTIC TEST ready. tenant=%s devices=%d", tenant.Name(), len(devices))

	go runPublisher(ctx, registry, *publishInterval)

	for {
		select {
		case <-ctx.Done():
			logger.Infof("Terminating diagnostics example")
			return
		case err := <-app.Error:
			logger.Errorf("Parent app error: %v", err)
		case err := <-appInstance.Error:
			logger.Errorf("App instance error: %v", err)
		}
	}
}
