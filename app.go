package cloud

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
	"github.com/google/uuid"
)

const (
	// newAppInstancePath creates and links the tenant at the same time
	newAppInstancePath    = "/idm/api/v1/appregistry/otp/new"
	createAppInstancePath = "/idm/api/v1/appregistry/applications/%s/instances"
	deleteAppInstancePath = "/idm/api/v1/appregistry/applications/%s"
	redeemPath            = "/idm/api/v1/appregistry/otp/redeem"
	unlinkPath            = "/idm/api/v1/appregistry/applications/%s/tenants/%s"
	getDevicesPath        = "/api/uno/v1/registry/devices"
	directModePath        = "/api/dxhub/v2/apiproxy/request/%s/direct%s"
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

	// Hostnames of the regional cloud environments
	RegionalFQDNs []string

	// Hostname of the global cloud environment
	GlobalFQDN string

	// ReadStreamID is the stream with "R" access obtained during app onboarding
	ReadStreamID string

	// WriteStreamID is the stream with "W" access obtained during app onboarding
	WriteStreamID string

	// GroupID defines the group in which this instance of the App belongs to. Instances that belong
	// in the same group gets messages distributed between them. Instances that belong in separate
	// groups get a copy of each message. If left empty, unique ID will be used.
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
	// Either use this or ApiKey
	GetCredentials func() (*Credentials, error)

	// ApiKey is used when GetCredentials is not specified
	ApiKey string

	// DeviceActivationHandler notifies when a device is activated
	DeviceActivationHandler func(device *Device)

	// DeviceDeactivationHandler notifies when a device is deactivated
	DeviceDeactivationHandler func(device *Device)

	// TenantUnlinkedHandler notifies when a tenant is unlinked from the cloud instead of app calling UnlinkTenant
	// Not providing a linked handler because it can only be triggered by calling LinkTenant
	// The stored tenant ID, name and token should be discarded
	TenantUnlinkedHandler func(tenant *Tenant)

	// DeviceMessageHandler is invoked when a new data message is received
	DeviceMessageHandler func(messageID string, device *Device, stream string, payload []byte)
}

// App represents an instance of a pxGrid Cloud Application
// App struct is the entry point for the pxGrid Cloud Go SDK
type App struct {
	config     Config
	httpClient *resty.Client        // global HTTP client
	conn       []*pubsub.Connection // pubsub WebSocket connection

	tenantMap sync.Map
	deviceMap sync.Map

	// Error channel should be used to monitor any errors
	Error                  chan error
	wg                     sync.WaitGroup
	ctx                    context.Context
	ctxCancel              context.CancelFunc
	startPubsubConnectOnce sync.Once
}

var (
	defaultHTTPScheme = "https"
)

func (app *App) String() string {
	return fmt.Sprintf("App[ID: %s, RegionalFQDNs: %v]", app.config.ID, app.config.RegionalFQDNs)
}

// New creates and returns a new instance of App
// New accepts Config argument which is used to construct http clients, transport layer and setup PubSub configuration
func New(config Config) (*App, error) {
	if err := validateConfig(&config); err != nil {
		log.Logger.Errorf("Invalid configuration: %v", err)
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	hostURL := url.URL{
		Scheme: defaultHTTPScheme,
		Path:   url.PathEscape(config.GlobalFQDN),
	}

	httpClient := resty.New().
		SetBaseURL(hostURL.String()).
		OnBeforeRequest(func(_ *resty.Client, request *resty.Request) error {
			var key string
			if config.GetCredentials != nil {
				credentials, err := config.GetCredentials()
				if err != nil {
					return err
				}
				key = string(credentials.ApiKey)
				zeroByteArray(credentials.ApiKey)
			} else {
				key = config.ApiKey
			}
			request.SetHeader("X-Api-Key", key)
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

	app.ctx, app.ctxCancel = context.WithCancel(context.Background())
	return app, nil
}

func validateConfig(config *Config) error {
	// sanitize all the input
	config.ID = strings.TrimSpace(config.ID)
	if len(config.RegionalFQDNs) == 0 {
		config.RegionalFQDNs[0] = strings.TrimSpace(config.RegionalFQDN)
	} else {
		for i, regionalFQDN := range config.RegionalFQDNs {
			config.RegionalFQDNs[i] = strings.TrimSpace(regionalFQDN)
		}
	}
	config.GlobalFQDN = strings.TrimSpace(config.GlobalFQDN)
	config.ReadStreamID = strings.TrimSpace(config.ReadStreamID)
	config.WriteStreamID = strings.TrimSpace(config.WriteStreamID)
	config.GroupID = strings.TrimSpace(config.GroupID)

	if config.ID == "" {
		return errors.New("ID must not be empty")
	}
	for _, regionalFQDN := range config.RegionalFQDNs {
		if regionalFQDN == "" {
			return errors.New("RegionalFQDN must not be empty")
		}
	}

	if config.GlobalFQDN == "" {
		return errors.New("GlobalFQDN must not be empty")
	}

	if config.ReadStreamID == "" || config.WriteStreamID == "" {
		return errors.New("ReadStreamID and WriteStreamID must not be empty")
	}

	if config.GroupID == "" {
		config.GroupID = uuid.NewString()
	}

	return nil
}

// Close shuts down the App instance and releases all the resources
func (app *App) Close() error {
	if app.conn != nil {
		for _, connection := range app.conn {
			connection.Disconnect()
		}
	}
	app.ctxCancel()
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

// Report error to app.Error as non-blocking channel
func (app *App) reportError(err error) {
	select {
	case app.Error <- err:
	default:
		log.Logger.Warnf("Error message was not sent as the channel was not available")
	}
}

// Close shuts down the App instance and releases all the resources
func (app *App) close() error {
	if app.conn != nil {
		for _, connection := range app.conn {
			connection.Disconnect()
		}
	}

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

// pubsubConnect opens a websocket connection to pxGrid Cloud
func (app *App) pubsubConnect() error {
	var err error
	for _, fqdn := range app.config.RegionalFQDNs {

		connection, connectionErr := pubsub.NewConnection(pubsub.Config{
			GroupID: app.config.GroupID,
			Domain:  url.PathEscape(fqdn),
			APIKeyProvider: func() ([]byte, error) {
				if app.config.GetCredentials != nil {
					credentials, e := app.config.GetCredentials()
					if e != nil {
						return nil, e
					}
					return credentials.ApiKey, e
				} else {
					return []byte(app.config.ApiKey), nil
				}
			},
			Transport: app.config.Transport,
		})
		if connectionErr != nil {
			return fmt.Errorf("failed to create pubsub connection: %v", connectionErr)
		}
		app.conn = append(app.conn, connection)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for _, connection := range app.conn {
		err = connection.Connect(ctx)
		if err != nil {
			return fmt.Errorf("failed to connect pubsub connection: %v", err)
		}

		err = connection.Subscribe(app.config.ReadStreamID, app.readStreamHandler())
		if err != nil {
			return fmt.Errorf("failed to subscribe: %v", err)
		}
	}

	return nil
}

const (
	msgType              = "messageType"
	msgTypeControl       = "control"
	msgTypeData          = "data"
	tenantKey            = "tenant"
	deviceKey            = "device"
	msgIDKey             = "messageID"
	msgTypeActivate      = "device:activate"
	msgTypeDeactivate    = "device:deactivate"
	msgTypeAppConnect    = "app:connect"
	msgTypeAppDisconnect = "app:disconnect"
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
	log.Logger.Debugf("Received control message: %v", ctrlPayload)

	v, ok := app.deviceMap.Load(ctrlPayload.Info.Tenant)
	if !ok || v == nil {
		log.Logger.Debugf("Unassociated tenant: %s", ctrlPayload.Info.Tenant)
		return nil
	}
	deviceMap := v.(*sync.Map)

	if ctrlPayload.Type == msgTypeActivate {
		v, ok = app.tenantMap.Load(ctrlPayload.Info.Tenant)
		if !ok || v == nil {
			return fmt.Errorf("unknown tenant: %s", ctrlPayload.Info.Tenant)
		}
		tenant := v.(*Tenant)
		device, err := tenant.getDeviceByID(ctrlPayload.Info.Device)
		if err != nil {
			return fmt.Errorf("failed to get device %s info: %w", ctrlPayload.Info.Device, err)
		}
		deviceMap.Store(device.ID(), device)
		if app.config.DeviceActivationHandler != nil {
			app.config.DeviceActivationHandler(device)
		}
	} else if ctrlPayload.Type == msgTypeDeactivate {
		v, ok = deviceMap.Load(ctrlPayload.Info.Device)
		if !ok || v == nil {
			return fmt.Errorf("unknown device: %s", ctrlPayload.Info.Device)
		}
		device := v.(*Device)
		deviceMap.Delete(device.ID())
		if app.config.DeviceDeactivationHandler != nil {
			app.config.DeviceDeactivationHandler(device)
		}
	} else if ctrlPayload.Type == msgTypeAppConnect {
		// Ignore app connect message because app calls LinkTenant explicitly
	} else if ctrlPayload.Type == msgTypeAppDisconnect {
		v, ok = app.tenantMap.Load(ctrlPayload.Info.Tenant)
		if !ok || v == nil {
			return fmt.Errorf("unknown tenant: %s", ctrlPayload.Info.Tenant)
		}
		tenant := v.(*Tenant)
		app.tenantMap.Delete(tenant.ID())
		app.deviceMap.Delete(tenant.ID())
		if app.config.TenantUnlinkedHandler != nil {
			app.config.TenantUnlinkedHandler(tenant)
		}
	} else {
		return fmt.Errorf("unknown control message type: %s", ctrlPayload.Type)
	}
	return nil
}

func (app *App) dataMsgHandler(id string, headers map[string]string, payload []byte) error {
	log.Logger.Debugf("Received data message: %s, device: %s, tenant: %s -- %s",
		headers[msgIDKey], headers[deviceKey], headers[tenantKey], payload)
	if _, ok := headers[deviceKey]; !ok {
		return fmt.Errorf("data missing device id")
	}
	if _, ok := headers[tenantKey]; !ok {
		return fmt.Errorf("data missing tenant id")
	}

	v, ok := app.deviceMap.Load(headers[tenantKey])
	if !ok || v == nil {
		log.Logger.Debugf("Unassociated tenant %s", headers[tenantKey])
		return nil
	}
	deviceMapInternal := v.(*sync.Map)

	v, ok = deviceMapInternal.Load(headers[deviceKey])
	if !ok || v == nil {
		return fmt.Errorf("unknown device: %s", headers[deviceKey])
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
// The stored tenant ID, name and token should be discarded
func (app *App) UnlinkTenant(tenant *Tenant) error {
	// Remove from map first to prevent callback to TenantUnlinkedHandler
	app.tenantMap.Delete(tenant.ID())
	app.deviceMap.Delete(tenant.ID())

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
	regionalHttpClients := make(map[string]*resty.Client)
	for _, regionalFQDN := range app.config.RegionalFQDNs {
		httpClient := resty.NewWithClient(app.httpClient.GetClient()).
			SetBaseURL(app.httpClient.HostURL)
		tenant.setHttpClient(httpClient)

		regionalHostURL := url.URL{
			Scheme: defaultHTTPScheme,
			Path:   url.PathEscape(regionalFQDN),
		}
		regionalHttpClient := resty.NewWithClient(app.httpClient.GetClient()).
			SetBaseURL(regionalHostURL.String())
		regionalHttpClients[app.config.RegionalFQDN] = regionalHttpClient
	}
	tenant.setRegionalHttpClients(regionalHttpClients)
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
	}
	app.deviceMap.Store(tenant.id, &deviceMapInternal)

	// Once a tenant is added, we can start pubsub
	app.startPubsubConnect()
	return nil
}

func (app *App) loadTenantsDevices() {
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

func (app *App) startPubsubConnect() {
	app.startPubsubConnectOnce.Do(func() {
		app.wg.Add(1)
		go func() {
			defer app.wg.Done()
			backoffFactor := 0
			maxbackoffFactor := 3
			reconnectBackoff := 30 * time.Second
			reconnectDelay := 30 * time.Second

			//loop to call app.connect with a reconnect delay with gradual backoff
			for {
				err := app.pubsubConnect()
				if err != nil {
					log.Logger.Errorf("Failed to connect the app: %v", err)
					app.close()
					app.reportError(err)
				} else {
					//obtain the device list
					app.loadTenantsDevices()
					//reset backoff factor for successful connection
					backoffFactor = 0

					for _, connection := range app.conn {
						select {
						case err = <-connection.Error:
							app.close()
							app.reportError(err)
						case <-app.ctx.Done():
							return
						}
					}
				}

				select {
				case <-time.After(reconnectDelay + reconnectBackoff*time.Duration(backoffFactor)):
					//increment backoff factor by 1 for gradual backoff
					if backoffFactor < maxbackoffFactor {
						backoffFactor += 1
					}
				case <-app.ctx.Done():
					return
				}
			}
		}()
	})
}
