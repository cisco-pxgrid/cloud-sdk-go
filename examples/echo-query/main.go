package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"

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
	file := flag.String("in", "", "File for input for echo (optional). stdin if not specified")
	out := flag.String("out", "", "File for output for echo (optional). stdout if not specified")
	url := flag.String("url", "", "request url")
	method := flag.String("method", "", "request type")
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

	var filteredDevices []sdk.Device
	//Filter the devices based on the configured regions
	for _, device := range devices {
		if len(appConfig.RegionalFQDNs) != 0 {
			for _, configuredRegionalFQDN := range appConfig.RegionalFQDNs {
				if device.Fqdn() == configuredRegionalFQDN {
					filteredDevices = append(filteredDevices, device)
				}
			}
		} else {
			if device.Fqdn() == appConfig.RegionalFQDN {
				filteredDevices = append(filteredDevices, device)
			}
		}
	}

	logger.Infof("List of devices for the configured regions are %v", filteredDevices)

	// Setup output
	var writer io.Writer
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			panic(err)
		}
		defer f.Close()
		writer = f
	} else {
		writer = os.Stdout
	}

	// Loop through all the devices of the configured regions
	for i := 0; i < len(filteredDevices); i++ {
		device := filteredDevices[i]
		logger.Infof("Selected device name=%s tenant=%s id=%s region=%s", device.Name(), device.Tenant().Name(), device.ID(), device.Region())

		// Setup input
		var reader io.Reader
		if *file != "" {
			f, err := os.Open(*file)
			if err != nil {
				panic(err)
			}
			defer f.Close()
			reader = f
		} else {
			reader = os.Stdin
		}

		if *method == "" {
			*method = http.MethodPost
		}
		if *url == "" {
			*url = "/pxgrid/echo/query"
		}

		// Perform api request
		req, _ := http.NewRequest(*method, *url, reader)
		resp, err := device.Query(req)
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()

		// Write body to output
		n, err := io.Copy(writer, resp.Body)
		if err != nil {
			panic(err)
		}
		fmt.Println()
		logger.Infof("Query completed. status=%s bodyLen=%d\n", resp.Status, n)
	}

	if err = app.Close(); err != nil {
		panic(err)
	}
}
