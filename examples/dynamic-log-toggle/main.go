// Command dynamic-log-toggle demonstrates driving App.SetLogEachMessage from the
// PXCLOUD_SDK_DEBUG environment variable (TRUE/FALSE), without restarting the SDK App.
//
// IMPORTANT: a running process's environment variables are fixed at startup. Setting
// PXCLOUD_SDK_DEBUG in the shell (or a container's env) after this process is already running
// does NOT change what os.Getenv sees here - that's an OS-level limitation, not an SDK one.
// The poll loop below will only observe a change if something inside this same process calls
// os.Setenv (useful for local testing/demos). For a genuine external, no-restart control plane,
// wire app.SetLogEachMessage into an HTTP admin endpoint or a watched file instead - see README.md.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	sdk "github.com/cisco-pxgrid/cloud-sdk-go"
	"github.com/cisco-pxgrid/cloud-sdk-go/log"
	"gopkg.in/yaml.v2"
)

var logger = &log.DefaultLogger{Level: log.LogLevelInfo}

const debugEnvVar = "PXCLOUD_SDK_DEBUG"

type appConfig struct {
	Id            string   `yaml:"id"`
	ApiKey        string   `yaml:"apiKey"`
	GlobalFQDN    string   `yaml:"globalFQDN"`
	RegionalFQDN  string   `yaml:"regionalFQDN"`
	RegionalFQDNs []string `yaml:"regionalFQDNs"`
	ReadStream    string   `yaml:"readStream"`
	WriteStream   string   `yaml:"writeStream"`
	GroupId       string   `yaml:"groupId"`
	// LogEachMessage is the startup default; PXCLOUD_SDK_DEBUG overrides it when explicitly set.
	LogEachMessage bool `yaml:"logEachMessage"`
}

type tenantConfig struct {
	Id    string `yaml:"id"`
	Name  string `yaml:"name"`
	Token string `yaml:"token"`
}

type appInstanceConfig struct {
	Otp    string       `yaml:"otp"`
	Name   string       `yaml:"name"`
	Id     string       `yaml:"id"`
	ApiKey string       `yaml:"apiKey"`
	Tenant tenantConfig `yaml:"tenant"`
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
	c := config{}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *config) store(file string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0600)
}

// readDebugEnv parses PXCLOUD_SDK_DEBUG (TRUE/FALSE, case-insensitive). ok is false when the
// variable is unset or unparsable, in which case callers should fall back to another source.
func readDebugEnv() (enabled bool, ok bool) {
	v, present := os.LookupEnv(debugEnvVar)
	if !present {
		return false, false
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return false, false
	}
	return parsed, true
}

// watchDebugEnv re-checks PXCLOUD_SDK_DEBUG on a timer and whenever SIGHUP is received, applying
// any change via app.SetLogEachMessage. See the package doc comment for why this only reacts to
// os.Setenv calls made inside this process, not external env changes.
func watchDebugEnv(ctx context.Context, app *sdk.App, pollInterval time.Duration, current bool) {
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)
	defer signal.Stop(reload)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-reload:
		case <-ticker.C:
		}
		if enabled, ok := readDebugEnv(); ok && enabled != current {
			current = enabled
			app.SetLogEachMessage(current)
			logger.Infof("%s changed, applied without restart. logEachMessage=%t", debugEnvVar, current)
		}
	}
}

func messageHandler(id string, d *sdk.Device, stream string, p []byte) {
	logger.Infof("Message received. tenant=%s device=%s stream=%s message=%s", d.Tenant().Name(), d.Name(), stream, string(p))
}

func main() {
	configFile := flag.String("config", "", "Configuration yaml file to use (required)")
	insecure := flag.Bool("insecure", false, "Insecure TLS")
	group := flag.String("group", "", "Group ID")
	pollInterval := flag.Duration("poll-interval", 5*time.Second, "How often to re-check "+debugEnvVar)
	flag.Parse()
	if *pollInterval <= 0 {
		fmt.Fprintln(os.Stderr, "-poll-interval must be greater than zero")
		os.Exit(2)
	}

	cfg, err := loadConfig(*configFile)
	if err != nil {
		panic(err)
	}
	log.Logger = logger

	initialDebug := cfg.App.LogEachMessage
	if enabled, ok := readDebugEnv(); ok {
		initialDebug = enabled
	}
	groupID := cfg.App.GroupId
	if *group != "" {
		groupID = *group
	}
	logger.Infof("Startup config.logEachMessage=%t %s=%q -> logEachMessage=%t", cfg.App.LogEachMessage, debugEnvVar, os.Getenv(debugEnvVar), initialDebug)

	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           d.DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: *insecure},
	}

	appConfig := sdk.Config{
		ID: cfg.App.Id,
		GetCredentials: func() (*sdk.Credentials, error) {
			return &sdk.Credentials{ApiKey: []byte(cfg.App.ApiKey)}, nil
		},
		GlobalFQDN:           cfg.App.GlobalFQDN,
		RegionalFQDN:         cfg.App.RegionalFQDN,
		RegionalFQDNs:        cfg.App.RegionalFQDNs,
		ReadStreamID:         cfg.App.ReadStream,
		WriteStreamID:        cfg.App.WriteStream,
		GroupID:              groupID,
		Transport:            transport,
		LogEachMessage:       &initialDebug,
		DeviceMessageHandler: messageHandler,
	}

	app, err := sdk.New(appConfig)
	if err != nil {
		panic(err)
	}
	defer app.Close()

	ac := &cfg.AppInstance
	tc := &ac.Tenant
	var tenant *sdk.Tenant
	var appInstance *sdk.App
	if ac.Otp != "" {
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
		if err := cfg.store(*configFile); err != nil {
			logger.Errorf("Failed to store config: %v", err)
		}
	} else {
		appInstance, err = app.SetAppInstance(ac.Id, ac.ApiKey)
		if err != nil {
			logger.Errorf("Failed to set app instance: %v", err)
			os.Exit(-1)
		}
		tenant, err = appInstance.SetTenant(tc.Id, tc.Name, tc.Token)
		if err != nil {
			logger.Errorf("Failed to set tenant to app: %v", err)
			os.Exit(-1)
		}
	}
	defer appInstance.Close()
	logger.Infof("Linked with tenant: %s", tenant.Name())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// appInstance carries the live subscription, so the toggle is applied there.
	go watchDebugEnv(ctx, appInstance, *pollInterval, initialDebug)

	for {
		select {
		case err := <-appInstance.Error:
			logger.Errorf("App error: %v", err)
		case <-ctx.Done():
			logger.Infof("Terminating...")
			return
		}
	}
}
