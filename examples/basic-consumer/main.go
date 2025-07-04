package main

import (
	"context"
	"crypto/tls"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cisco-pxgrid/cloud-sdk-go/log"
	"gopkg.in/yaml.v2"

	sdk "github.com/cisco-pxgrid/cloud-sdk-go"
)

var logger *log.DefaultLogger = &log.DefaultLogger{Level: log.LogLevelInfo}

type appConfig struct {
	Id            string   `yaml:"id"`
	ApiKey        string   `yaml:"apiKey"`
	GlobalFQDN    string   `yaml:"globalFQDN"`
	RegionalFQDN  string   `yaml:"regionalFQDN"`
	RegionalFQDNs []string `yaml:"regionalFQDNs"`
	ReadStream    string   `yaml:"readStream"`
	WriteStream   string   `yaml:"writeStream"`
	GroupId       string   `yaml:"groupId"`
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
	logger.Infof("Message received. tenant=%s device=%s stream=%s message=%s\n", d.Tenant().Name(), d.Name(), stream, string(p))
}

func activationHandler(d *sdk.Device) {
	logger.Infof("Device activation: %v", d)
}

func deactivationHandler(d *sdk.Device) {
	logger.Infof("Device deactivation: %v", d)
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

func main() {
	// Load config
	configFile := flag.String("config", "", "Configuration yaml file to use (required)")
	debug := flag.Bool("debug", false, "Enable debug output")
	insecure := flag.Bool("insecure", false, "Insecure TLS")
	group := flag.String("group", "", "Group ID")

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
	}
	// SDK App create
	app, err := sdk.New(appConfig)
	if err != nil {
		panic(err)
	}
	defer app.Close()
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
	logger.Infof("Linked with tenant: %s", tenant.Name())

	// Catch termination signal
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	for {
		select {
		case err := <-app.Error:
			logger.Errorf("App error: %v", err)
		case <-ctx.Done():
			logger.Infof("Terminating...")
			return
		}
	}
}
