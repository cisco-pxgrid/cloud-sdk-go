package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/cisco-pxgrid/cloud-sdk-go/log"
	"gopkg.in/yaml.v2"

	sdk "github.com/cisco-pxgrid/cloud-sdk-go"
)

var logger *log.DefaultLogger = &log.DefaultLogger{Level: log.LogLevelInfo}

type appConfig struct {
	Id           string `yaml:"id"`
	ApiKey       string `yaml:"apiKey"`
	GlobalFQDN   string `yaml:"globalFQDN"`
	RegionalFQDN string `yaml:"regionalFQDN"`
	ReadStream   string `yaml:"readStream"`
	WriteStream  string `yaml:"writeStream"`
}

type tenantConfig struct {
	Otp   string `yaml:"otp"`
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	Token string `yaml:"token"`
}

type config struct {
	App    appConfig    `yaml:"app"`
	Tenant tenantConfig `yaml:"tenant"`
}

func messageHandler(id string, d *sdk.Device, stream string, p []byte) {
	logger.Infof("Received message id=%s tenant=%s device=%s stream=%s payload=%s",
		id, d.Tenant().Name(), d.Name(), stream, string(p))
}

func activationHandler(d *sdk.Device) {
	logger.Infof("Device activation: %v", d)
}

func deActivationHandler(d *sdk.Device) {
	logger.Infof("Device deactivation: %v", d)
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

func main() {
	// Load config
	configFile := flag.String("config", "", "Configuration yaml file to use (required)")
	debug := flag.Bool("debug", false, "Enable debug output")
	insecure := flag.Bool("insecure", false, "Insecure TLS")
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
	appConfig := sdk.Config{
		ID:                        config.App.Id,
		GetCredentials:            getCredentials,
		GlobalFQDN:                config.App.GlobalFQDN,
		RegionalFQDN:              config.App.RegionalFQDN,
		DeviceActivationHandler:   activationHandler,
		DeviceDeactivationHandler: deActivationHandler,
		DeviceMessageHandler:      messageHandler,
		ReadStreamID:              config.App.ReadStream,
		WriteStreamID:             config.App.WriteStream,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: *insecure,
			},
		},
	}
	// SDK App create
	app, err := sdk.New(appConfig)
	if err != nil {
		panic(err)
	}
	logger.Debugf("App config: %+v", appConfig)

	var tc = &config.Tenant
	var tenant *sdk.Tenant
	if tc.Otp != "" {
		// SDK link tenant with OTP
		tenant, err = app.LinkTenant(tc.Otp)
		if err != nil {
			logger.Errorf("Failed to link tenant: %v", err)
			os.Exit(-1)
		}
		tc.Otp = ""
		tc.ID = tenant.ID()
		tc.Name = tenant.Name()
		tc.Token = tenant.ApiToken()
		config.store(*configFile)
	} else {
		// SDK set tenant with existing id, name and token
		tenant, err = app.SetTenant(tc.ID, tc.Name, tc.Token)
		if err != nil {
			logger.Errorf("Failed to set tenant to app: %v", err)
			os.Exit(-1)
		}
	}

	// SDK get devices
	devices, err := tenant.GetDevices()
	if err != nil {
		logger.Errorf("Failed to get devices: %v", err)
		os.Exit(-1)
	}
	if len(devices) == 0 {
		logger.Errorf("No device found. tenant=%s", tenant.Name())
		os.Exit(-1)
	}
	// Select first device
	device := devices[0]
	logger.Infof("Selected first device name=%s tenant=%s id=%s ", device.Name(), device.Tenant().Name(), device.ID())

	// Query
	go func() {
		reader := bufio.NewReader(os.Stdin)
		for {
			fmt.Printf("Enter q to getSessions: ")
			text, _ := reader.ReadString('\n')
			text = strings.Replace(text, "\n", "", -1)
			if text == "q" {
				req, _ := http.NewRequest(http.MethodPost, "/pxgrid/session/getSessions", bytes.NewBuffer([]byte("{}")))
				resp, err := device.Query(req)
				if err != nil {
					logger.Errorf("Failed to invoke %s on %s: %v", req, device, err)
				} else {
					bodyBytes, err := io.ReadAll(resp.Body)
					if err != nil {
						logger.Errorf("Failed to read response body: %v", err)
					}
					bodyString := string(bodyBytes)
					logger.Infof("Status=%s Body=%s", resp.Status, bodyString)
				}
			}
		}
	}()

	// Catch termination signal
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
		logger.Infof("Terminating...")
	case err := <-app.Error:
		logger.Errorf("App error: %v", err)
	}
	if err = app.Close(); err != nil {
		panic(err)
	}
}
