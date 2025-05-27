package main

import (
	"context"
	"crypto/tls"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"

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
	deleteInstance := flag.Bool("delete", false, "Delete app instance")
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
		RegionalFQDNs:             config.App.RegionalFQDNs,
		DeviceActivationHandler:   activationHandler,
		DeviceDeactivationHandler: deactivationHandler,
		TenantUnlinkedHandler:     tenantUnlinkedHandler,
		DeviceMessageHandler:      messageHandler,
		ReadStreamID:              config.App.ReadStream,
		WriteStreamID:             config.App.WriteStream,
		GroupID:                   *group,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: *insecure,
			},
			Proxy: http.ProxyFromEnvironment,
		},
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

	// SDK get devices
	devices, err := tenant.GetDevices()
	if err != nil {
		logger.Errorf("Failed to get devices: %v", err)
		os.Exit(-1)
	}
	if len(devices) > 0 {
		for _, d := range devices {
			logger.Infof("Activated device: %v", d.Name())
		}
	} else {
		logger.Infof("No device found yet")
	}

	// Catch termination signal
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
		logger.Infof("Terminating...")
	case err := <-appInstance.Error:
		logger.Errorf("AppInstance error: %v", err)
	case err := <-app.Error:
		logger.Errorf("App error: %v", err)
	}

	if err = appInstance.Close(); err != nil {
		panic(err)
	}
	if err = app.Close(); err != nil {
		panic(err)
	}
}
