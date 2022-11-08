package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cisco-pxgrid/cloud-sdk-go/internal/pubsub"
	"github.com/cisco-pxgrid/cloud-sdk-go/log"
	"github.com/go-resty/resty/v2"
)

const (
	redeemPath     = "/idm/api/v1/appregistry/otp/redeem"
	unlinkPath     = "/idm/api/v1/appregistry/applications/%s/tenants/%s"
	getDevicesPath = "/api/uno/v1/registry/devices"
	directModePath = "/api/dxhub/v2/apiproxy/request/%s/direct%s"
)

var tlsConfig = tls.Config{
	ClientAuth:               tls.NoClientCert,
	MinVersion:               tls.VersionTLS12,
	PreferServerCipherSuites: true,
	CipherSuites: []uint16{
		tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		tls.TLS_RSA_WITH_AES_128_CBC_SHA,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
		tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
		tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
		tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	},
}

// Credentials are fields that is used for request authorization
// Credentials required to be stored securely
type Credentials struct {
	// ApiKey is obtained during app onboarding with dragonfly
	// ApiKey will be zeroed after use, therefore AppConfig.GetCredentials function should provide new structure every invocation
	ApiKey []byte
}

// Config defines the configuration for an application
type Config struct {
	// ID is the unique application identifier obtained during app onboarding
	ID string

	// Hostname of the regional cloud environment
	RegionalFQDN string

	// Hostname of the global cloud environment
	GlobalFQDN string

	// ReadStreamID is the stream with "R" access obtained during app onboarding
	ReadStreamID string

	// WriteStreamID is the stream with "W" access obtained during app onboarding
	WriteStreamID string

	// GroupID defines the group in which this instance of the App belongs to. Instances that belong
	// in the same group gets messages distributed between them. Instances that belong in separate
	// groups get a copy of each message. If left empty, all the instances will be placed in the
	// same group.
	//
	// e.g. There are 3 messages on the app's stream - msg1, msg2, msg3
	//
	// If there are 2 app instances of the same app with same group ID, the 3 messages are
	// distributed between both the instances.
	// If there are 2 app instances of the same app with different group IDs, then each instance
	// receives all 3 messages.
	GroupID string

	// Transport (if set) will be used for any HTTP connection establishment by the SDK
	Transport *http.Transport

	// GetCredentials is used to retrieve the client credentials provided to the app during onboarding
	GetCredentials func() (*Credentials, error)

	// DeviceActivationHandler notifies when a device is activated
	DeviceActivationHandler func(device *Device)

	// DeviceDeactivationHandler notifies when a device is deactivated
	DeviceDeactivationHandler func(device *Device)

	// DeviceMessageHandler is invoked when a new data message is received
	DeviceMessageHandler func(messageID string, device *Device, stream string, payload []byte)
}

// App represents an instance of a pxGrid Cloud Application
// App struct is the entry point for the pxGrid Cloud Go SDK
type App struct {
	config     Config
	httpClient *resty.Client      // global HTTP client
	conn       *pubsub.Connection // pubsub WebSocket connection

	tenantMap sync.Map
	deviceMap sync.Map

	// Error channel should be used to monitor any errors
	Error chan error
	wg    sync.WaitGroup
}

func (app *App) String() string {
	return fmt.Sprintf("App[ID: %s, RegionalFQDN: %s]", app.config.ID, app.config.RegionalFQDN)
}

// New creates and returns a new instance of App
// New accepts Config argument which is used to construct http clients, transport layer and setup PubSub configuration
func New(config Config) (*App, error) {
	if err := validateConfig(&config); err != nil {
		log.Logger.Errorf("Invalid configuration: %v", err)
		return nil, fmt.Errorf("Invalid configuration: %w", err)
	}

	hostURL := url.URL{
		Scheme: "https",
		Path:   url.PathEscape(config.GlobalFQDN),
	}

	httpClient := resty.New().
		SetBaseURL(hostURL.String()).
		OnBeforeRequest(func(_ *resty.Client, request *resty.Request) error {
			credentials, err := config.GetCredentials()
			if err != nil {
				return err
			}

			request.SetHeader("X-API-KEY", string(credentials.ApiKey))
			zeroByteArray(credentials.ApiKey)
			return nil
		})

	if config.Transport == nil {
		httpClient.SetTLSClientConfig(&tlsConfig)
	} else {
		httpClient.SetTransport(config.Transport)
	}

	app := &App{
		config:     config,
		httpClient: httpClient,
		tenantMap:  sync.Map{},
		deviceMap:  sync.Map{},
		Error:      make(chan error, 1), // make sure the channel is buffered so that SDK doesn't block
		wg:         sync.WaitGroup{},
	}

	err := app.connect("")
	if err != nil {
		log.Logger.Errorf("Failed to connect the app: %v", err)
		return nil, err
	}

	app.wg.Add(1)
	go func() {
		defer app.wg.Done()
		var reconnectDelay = 1

		//loop to call app.connect with a reconnect delay with gradual backoff
		for {
			select {
			case err = <-app.conn.Error:
				//app.Error is just a channel to report errors. Developer can simply log it or ignore it.
				//app.Error channel as non-blocking
				app.Error <- err
				time.Sleep(time.Second * time.Duration(reconnectDelay))

				err := app.connect("")
				if err != nil {
					reconnectDelay += 1
					app.conn.Error <- err
					continue
				}
				//obtain the device list again when reconnected
				app.reloadTenantsDevices()
				reconnectDelay = 1
			}
		}
	}()

	return app, nil
}

func validateConfig(config *Config) error {
	// sanitize all the input
	config.ID = strings.TrimSpace(config.ID)
	config.RegionalFQDN = strings.TrimSpace(config.RegionalFQDN)
	config.GlobalFQDN = strings.TrimSpace(config.GlobalFQDN)
	config.ReadStreamID = strings.TrimSpace(config.ReadStreamID)
	config.WriteStreamID = strings.TrimSpace(config.WriteStreamID)
	config.GroupID = strings.TrimSpace(config.GroupID)

	if config.ID == "" {
		return errors.New("ID must not be empty")
	}

	if config.RegionalFQDN == "" || config.GlobalFQDN == "" {
		return errors.New("RegionalFQDN and GlobalFQDN must not be empty")
	}

	if config.ReadStreamID == "" || config.WriteStreamID == "" {
		return errors.New("ReadStreamID and WriteStreamID must not be empty")
	}

	if config.GroupID == "" {
		config.GroupID = config.ID
	}

	return nil
}

// Close shuts down the App instance and releases all the resources
func (app *App) Close() error {
	if app.conn != nil {
		app.conn.Disconnect()
	}
	app.wg.Wait()

	app.tenantMap.Range(func(key interface{}, _ interface{}) bool {
		app.tenantMap.Delete(key)
		return true
	})

	app.deviceMap.Range(func(key interface{}, _ interface{}) bool {
		app.deviceMap.Delete(key)
		return true
	})

	return nil
}

// connect opens a websocket connection to pxGrid Cloud
// WORKAROUND provide an option to use previous subscription ID
func (app *App) connect(subscriptionID string) error {
	var err error
	app.conn, err = pubsub.NewConnection(pubsub.Config{
		GroupID: app.config.GroupID,
		Domain:  url.PathEscape(app.config.RegionalFQDN),
		APIKeyProvider: func() ([]byte, error) {
			credentials, e := app.config.GetCredentials()
			if e != nil {
				return nil, e
			}
			return credentials.ApiKey, e
		},
		Transport: app.config.Transport,
	})
	if err != nil {
		return fmt.Errorf("failed to create new pubsub connection: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err = app.conn.Connect(ctx)
	if err != nil {
		return fmt.Errorf("failed to open pubsub connect: %v", err)
	}

	err = app.conn.Subscribe(app.config.ReadStreamID, app.readStreamHandler())
	if err != nil {
		return fmt.Errorf("failed to subscribe to app read stream: %v", err)
	}
	return nil
}

const (
	msgType           = "messageType"
	msgTypeControl    = "control"
	msgTypeData       = "data"
	tenantKey         = "tenant"
	deviceKey         = "device"
	msgIDKey          = "messageID"
	msgTypeActivate   = "device:activate"
	msgTypeDeactivate = "device:deactivate"
)

// readStreamHandler returns the callback that handles messages received on the app's read stream
func (app *App) readStreamHandler() pubsub.SubscriptionCallback {
	return func(err error, id string, headers map[string]string, payload []byte) {
		if err != nil {
			log.Logger.Errorf("Received error for %s stream: %v", app.config.ReadStreamID, err)
			return
		}

		switch headers[msgType] {
		case msgTypeControl:
			if err := app.controlMsgHandler(id, payload); err != nil {
				log.Logger.Errorf("Failed to handle control message %s: %v", payload, err)
				return
			}
		case msgTypeData:
			if err := app.dataMsgHandler(id, headers, payload); err != nil {
				log.Logger.Errorf("Failed to handle data message %s: %v", payload, err)
			}
		default:
			log.Logger.Errorf("Invalid message %s: %s, headers: %#v", id, payload, headers)
			return
		}
	}
}

// controlPayload represents the payload received in a control type message from DxHub
type controlPayload struct {
	Type string `json:"type"`
	Info struct {
		Tenant  string   `json:"tenant"`
		Device  string   `json:"device"`
		Streams []string `json:"streams"`
	} `json:"info"`
}

func (app *App) controlMsgHandler(id string, payload []byte) error {
	var ctrlPayload controlPayload
	if err := json.Unmarshal(payload, &ctrlPayload); err != nil {
		return fmt.Errorf("failed to unmarshal control payload: %w", err)
	}
	log.Logger.Infof("Received control message: %v", ctrlPayload)

	v, ok := app.deviceMap.Load(ctrlPayload.Info.Tenant)
	if !ok || v == nil {
		return fmt.Errorf("failed to load device map for tenant %s", ctrlPayload.Info.Tenant)
	}
	deviceMap := v.(*sync.Map)

	if ctrlPayload.Type == msgTypeActivate {
		v, ok = app.tenantMap.Load(ctrlPayload.Info.Tenant)
		if !ok || v == nil {
			return fmt.Errorf("tenant %s not found", ctrlPayload.Info.Tenant)
		}
		tenant := v.(*Tenant)
		device, err := tenant.getDeviceByID(ctrlPayload.Info.Device)
		if err != nil {
			return fmt.Errorf("failed to fetch device information for %s: %v", ctrlPayload.Info.Device, err)
		}
		if app.config.DeviceActivationHandler != nil {
			app.config.DeviceActivationHandler(device)
		}

		deviceMap.Store(device.ID(), device)
	} else if ctrlPayload.Type == msgTypeDeactivate {
		v, ok = deviceMap.Load(ctrlPayload.Info.Device)
		if !ok || v == nil {
			return fmt.Errorf("device %s not found", ctrlPayload.Info.Device)
		}
		device := v.(*Device)
		if app.config.DeviceDeactivationHandler != nil {
			app.config.DeviceDeactivationHandler(device)
		}
		deviceMap.Delete(device.ID())
	}
	return nil
}

func (app *App) dataMsgHandler(id string, headers map[string]string, payload []byte) error {
	log.Logger.Debugf("Received data message: %s, device: %s, tenant: %s -- %s",
		headers[msgIDKey], headers[deviceKey], headers[tenantKey], payload)
	if _, ok := headers[deviceKey]; !ok {
		return fmt.Errorf("missing device id")
	}
	if _, ok := headers[tenantKey]; !ok {
		return fmt.Errorf("missing tenant id")
	}

	v, ok := app.deviceMap.Load(headers[tenantKey])
	if !ok || v == nil {
		return fmt.Errorf("tenant %s not found", headers[tenantKey])
	}
	deviceMapInternal := v.(*sync.Map)

	v, ok = deviceMapInternal.Load(headers[deviceKey])
	if !ok || v == nil {
		return fmt.Errorf("device %s not found", headers[deviceKey])
	}

	device := v.(*Device)
	if app.config.DeviceMessageHandler != nil {
		app.config.DeviceMessageHandler(headers[msgIDKey], device, headers["stream"], payload)
	}

	return nil
}

type redeemOTPRequest struct {
	OTP string `json:"otp"`
}

type redeemOTPResponse struct {
	Token      string `json:"api_token"`
	TenantID   string `json:"tenant_id"`
	TenantName string `json:"tenant_name"`
}

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func (e errorResponse) GetError() string {
	if len(e.Error) > 0 {
		return e.Error
	}
	return e.Message
}

// LinkTenant redeems the OTP and links a tenant to the application
// Returned tenant must be stored securely
func (app *App) LinkTenant(otp string) (*Tenant, error) {
	redeemReq := redeemOTPRequest{
		OTP: otp,
	}

	var redeemResp redeemOTPResponse
	var errorResp errorResponse

	response, err := app.httpClient.R().
		SetBody(redeemReq).
		SetResult(&redeemResp).
		SetError(&errorResp).
		Post(redeemPath)
	if err != nil {
		return nil, err
	}

	if response.IsError() {
		return nil, errors.New(errorResp.GetError())
	}

	tenant := &Tenant{
		id:       redeemResp.TenantID,
		name:     redeemResp.TenantName,
		apiToken: redeemResp.Token,
	}
	err = app.setTenant(tenant)
	if err != nil {
		log.Logger.Infof("Failed to fetch devices for %s, unlinking tenant", tenant)
		if err = app.UnlinkTenant(tenant); err != nil {
			log.Logger.Errorf("Failed to unlink %s", tenant)
		}
		return nil, err
	}

	return tenant, nil
}

// UnlinkTenant unlinks a tenant from the application
func (app *App) UnlinkTenant(tenant *Tenant) error {
	unlinkPath := fmt.Sprintf(
		unlinkPath,
		url.PathEscape(app.config.ID),
		url.PathEscape(tenant.ID()))

	var errorResp errorResponse

	response, err := app.httpClient.R().
		SetError(&errorResp).
		Delete(unlinkPath)
	if err != nil {
		return err
	}

	if response.IsError() {
		return errors.New(errorResp.GetError())
	}

	app.tenantMap.Delete(tenant.ID())
	app.deviceMap.Delete(tenant.ID())

	return nil
}

// SetTenant adds linked tenant to the application's inner infrastructure
// SetTenant should be used by application after restart to reload tenants back
func (app *App) SetTenant(id, name, apiToken string) (*Tenant, error) {
	tenant := &Tenant{
		id:       id,
		name:     name,
		apiToken: apiToken,
	}
	err := app.setTenant(tenant)
	if err != nil {
		return tenant, err
	}

	return tenant, nil
}

func (app *App) setTenant(tenant *Tenant) error {
	app.tenantMap.Store(tenant.id, tenant)

	httpClient := resty.NewWithClient(app.httpClient.GetClient()).
		SetBaseURL(app.httpClient.HostURL)
	tenant.setHttpClient(httpClient)

	regionalHostURL := url.URL{
		Scheme: "https",
		Path:   url.PathEscape(app.config.RegionalFQDN),
	}
	regionalHttpClient := resty.NewWithClient(app.httpClient.GetClient()).
		SetBaseURL(regionalHostURL.String())
	tenant.setRegionalHttpClient(regionalHttpClient)
	tenant.app = app

	devices, err := tenant.getDevices()
	if err != nil {
		return fmt.Errorf("failed to fetch devices for %s: %v", tenant, err)
	}
	deviceMapInternal := sync.Map{}
	for i := range devices {
		device := &devices[i]
		device.tenant = tenant
		deviceMapInternal.Store(device.id, device)
		if app.config.DeviceActivationHandler != nil {
			app.config.DeviceActivationHandler(device)
		}
	}
	app.deviceMap.Store(tenant.id, &deviceMapInternal)

	return nil
}

func (app *App) reloadTenantsDevices() {
	app.tenantMap.Range(func(key interface{}, _ interface{}) bool {
		tenant, ok := app.tenantMap.Load(key)
		if !ok || tenant == nil {
			return false
		}
		devices, err := tenant.(*Tenant).getDevices()
		if err != nil {
			return false
		}
		deviceMapInternal := sync.Map{}
		for i := range devices {
			device := &devices[i]
			device.tenant = tenant.(*Tenant)
			deviceMapInternal.Store(device.id, device)
			if app.config.DeviceActivationHandler != nil {
				app.config.DeviceActivationHandler(device)
			}
		}
		app.deviceMap.Store(tenant.(*Tenant).id, &deviceMapInternal)
		return true
	})
}
