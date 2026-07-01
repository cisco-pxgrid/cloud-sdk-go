package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/cisco-pxgrid/cloud-sdk-go/log"
	"gopkg.in/yaml.v2"

	sdk "github.com/cisco-pxgrid/cloud-sdk-go"
)

var logger *log.DefaultLogger = &log.DefaultLogger{Level: log.LogLevelInfo}

// Track active devices
var (
	activeDevices = make(map[string]*sdk.Device)
	deviceMutex   sync.RWMutex
)

type appConfig struct {
	Id            string   `yaml:"id"`
	ApiKey        string   `yaml:"apiKey"`
	GlobalFQDN    string   `yaml:"globalFQDN"`
	RegionalFQDN  string   `yaml:"regionalFQDN"`
	RegionalFQDNs []string `yaml:"regionalFQDNs"`
	ReadStream    string   `yaml:"readStream"`
	WriteStream   string   `yaml:"writeStream"`
}

type appInstanceConfig struct {
	Otp    string       `yaml:"otp"`
	Name   string       `yaml:"name"`
	Id     string       `yaml:"id"`
	ApiKey string       `yaml:"apiKey"`
	Tenant tenantConfig `yaml:"tenant"`
}

type tenantConfig struct {
	Id    string `yaml:"id"`
	Name  string `yaml:"name"`
	Token string `yaml:"token"`
}

type config struct {
	App         appConfig         `yaml:"app"`
	AppInstance appInstanceConfig `yaml:"appInstance"`
}

func messageHandler(id string, d *sdk.Device, stream string, p []byte) {
	logger.Infof("Message received. tenant=%s device=%s stream=%s message=%s\n", d.Tenant().Name(), d.Name(), stream, string(p))

	// Check if this is an echo topic message
	if stream == "com.cisco.ise.echo" {
		logger.Infof("Echo message received from topic: %s", string(p))
	}
}

func activationHandler(d *sdk.Device) {
	logger.Infof("Device activation: %v", d)

	// Add device to active devices map
	deviceMutex.Lock()
	activeDevices[d.ID()] = d
	deviceMutex.Unlock()

	// Publish an echo message immediately upon activation
	message := map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"message":   "Device just activated!",
		"device":    d.Name(),
		"tenant":    d.Tenant().Name(),
	}
	if err := publishEchoMessage(d, message); err != nil {
		logger.Errorf("Failed to publish echo message on activation: %v", err)
	} else {
		logger.Infof("Published echo message to newly activated device: %s", d.Name())
	}
}

func deactivationHandler(d *sdk.Device) {
	logger.Infof("Device deactivation: %v", d)

	// Remove device from active devices map
	deviceMutex.Lock()
	delete(activeDevices, d.ID())
	deviceMutex.Unlock()
}

func tenantUnlinkedHandler(t *sdk.Tenant) {
	logger.Infof("Tenant unlinked: %v", t)
}

func loadConfig(file string) (*config, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	c := config{}
	err = yaml.Unmarshal(data, &c)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *config) store(file string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0644)
}

func publishEchoMessage(device *sdk.Device, message map[string]interface{}) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, "/pxgrid/echo/publish", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := device.Query(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	logger.Infof("Echo publish response: status=%s body=%s", resp.Status, string(body))

	return nil
}

func main() {
	// Load config
	configFile := flag.String("config", "", "Configuration yaml file to use (required)")
	debug := flag.Bool("debug", false, "Enable debug output")
	insecure := flag.Bool("insecure", false, "Insecure TLS")
	group := flag.String("group", "", "Group ID")
	deleteInstance := flag.Bool("delete", false, "Delete app instance")
	statusInterval := flag.Duration("status-interval", 30*time.Second, "Read-stream status/summary log interval")
	gapThreshold := flag.Duration("gap-threshold", 2*time.Minute, "Read-stream no-message gap WARN threshold")
	logEachMessage := flag.Bool("log-each-message", true, "Log an INFO line for every read-stream message on arrival")
	flag.Parse()
	config, err := loadConfig(*configFile)
	if err != nil {
		panic(err)
	}

	// Set logger
	log.Logger = logger
	if *debug {
		logger.Level = log.LogLevelDebug
	}

	// Log after set logger
	logger.Debugf("Config: %+v", config)

	// SDK App config
	getCredentials := func() (*sdk.Credentials, error) {
		return &sdk.Credentials{
			ApiKey: []byte(config.App.ApiKey),
		}, nil
	}
	d := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	t := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           d.DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: *insecure,
		},
	}
	appConfig := sdk.Config{
		ID:                        config.App.Id,
		GetCredentials:            getCredentials,
		GlobalFQDN:                config.App.GlobalFQDN,
		RegionalFQDN:              config.App.RegionalFQDN,
		RegionalFQDNs:             config.App.RegionalFQDNs,
		DeviceActivationHandler:   activationHandler,
		DeviceDeactivationHandler: deactivationHandler,
		TenantUnlinkedHandler:     tenantUnlinkedHandler,
		DeviceMessageHandler:      messageHandler,
		ReadStreamID:              config.App.ReadStream,
		WriteStreamID:             config.App.WriteStream,
		GroupID:                   *group,
		Transport:                 t,
		StatusLogInterval:         *statusInterval,
		LogEachMessage:            logEachMessage,
		MessageGapThreshold:       *gapThreshold,
	}
	// SDK App create
	app, err := sdk.New(appConfig)
	if err != nil {
		panic(err)
	}
	logger.Debugf("App config: %+v", appConfig)

	var ac = &config.AppInstance
	var tc = &ac.Tenant

	if *deleteInstance {
		// SDK Delete app instance
		app.DeleteAppInstance(ac.Id)
		os.Exit(0)
	}

	var tenant *sdk.Tenant
	var appInstance *sdk.App
	if ac.Otp != "" {
		// SDK Link tenant with new app instance
		appInstance, err = app.CreateAppInstance(ac.Name)
		if err != nil {
			logger.Errorf("Failed to create app instance: %v", err)
			os.Exit(-1)
		}
		tenant, err = appInstance.LinkTenant(ac.Otp)
		if err != nil {
			logger.Errorf("Failed to link tenant: %v", err)
			os.Exit(-1)
		}
		ac.Otp = ""
		ac.Id = appInstance.ID()
		ac.ApiKey = appInstance.ApiKey()
		tc.Id = tenant.ID()
		tc.Name = tenant.Name()
		tc.Token = tenant.ApiToken()
		config.store(*configFile)
	} else {
		// SDK Set app instance with existing id and key
		appInstance, err = app.SetAppInstance(ac.Id, ac.ApiKey)
		if err != nil {
			logger.Errorf("Failed to set app: %v", err)
			os.Exit(-1)
		}

		// SDK set tenant with existing id, name and token
		tenant, err = appInstance.SetTenant(tc.Id, tc.Name, tc.Token)
		if err != nil {
			logger.Errorf("Failed to set tenant to app: %v", err)
			os.Exit(-1)
		}
	}

	// SDK get devices and populate active devices map
	devices, err := tenant.GetDevices()
	if err != nil {
		logger.Errorf("Failed to get devices: %v", err)
		os.Exit(-1)
	}

	deviceMutex.Lock()
	if len(devices) > 0 {
		for _, d := range devices {
			logger.Infof("Activated device: %v", d.Name())
			activeDevices[d.ID()] = &d
		}
	} else {
		logger.Infof("No device found yet")
	}
	deviceMutex.Unlock()

	// Catch termination signal
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Create a ticker to periodically publish echo messages
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	// Publish initial echo message to already activated devices
	deviceMutex.RLock()
	if len(activeDevices) > 0 {
		for _, device := range activeDevices {
			message := map[string]interface{}{
				"timestamp": time.Now().Format(time.RFC3339),
				"message":   "Hello from multi-instance example",
				"device":    device.Name(),
				"tenant":    tenant.Name(),
			}
			if err := publishEchoMessage(device, message); err != nil {
				logger.Errorf("Failed to publish echo message: %v", err)
			} else {
				logger.Infof("Published echo message to device: %s", device.Name())
			}
			break // Only send to first device for now
		}
	}
	deviceMutex.RUnlock()

	for {
		select {
		case <-ctx.Done():
			logger.Infof("Terminating...")
			goto cleanup
		case err := <-appInstance.Error:
			logger.Errorf("AppInstance error: %v", err)
			goto cleanup
		case err := <-app.Error:
			logger.Errorf("App error: %v", err)
			goto cleanup
		case <-ticker.C:
			// Periodically publish echo messages to all active devices
			deviceMutex.RLock()
			if len(activeDevices) > 0 {
				for _, device := range activeDevices {
					message := map[string]interface{}{
						"timestamp": time.Now().Format(time.RFC3339),
						"message":   "Periodic echo message",
						"device":    device.Name(),
						"tenant":    device.Tenant().Name(),
					}
					if err := publishEchoMessage(device, message); err != nil {
						logger.Errorf("Failed to publish periodic echo message: %v", err)
					} else {
						logger.Debugf("Published periodic echo message to device: %s", device.Name())
					}
				}
			} else {
				logger.Debugf("No active devices to send periodic messages")
			}
			deviceMutex.RUnlock()
		}
	}

cleanup:
	if err = appInstance.Close(); err != nil {
		panic(err)
	}
	if err = app.Close(); err != nil {
		panic(err)
	}
}
