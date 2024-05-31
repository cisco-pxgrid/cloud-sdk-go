package cloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sync"

	"github.com/go-resty/resty/v2"
)

// Tenant represents a tenant that has been linked to the application via OTP redemption
// This has to be stored securely by the application.
// During restart, application is required to reload it back to use using App.SetTenant function
type Tenant struct {
	id       string
	name     string
	apiToken string

	app                 *App
	httpClient          *resty.Client
	regionalHttpClients map[string]*resty.Client
}

func (t *Tenant) String() string {
	return fmt.Sprintf("Tenant[Name: %s, ID: %s]", t.name, t.id)
}

// ID returns tenant's id
func (t *Tenant) ID() string {
	return t.id
}

// Name returns tenant's name
func (t *Tenant) Name() string {
	return t.name
}

// ApiToken returns tenant's api token
func (t *Tenant) ApiToken() string {
	return t.apiToken
}

func (t *Tenant) getDevices() ([]Device, error) {
	var gdr []getDeviceResponse
	var errorResp errorResponse

	response, err := t.httpClient.R().
		SetResult(&gdr).
		SetError(&errorResp).
		Get(getDevicesPath)
	if err != nil {
		return nil, err
	}

	if response.IsError() {
		return nil, errors.New(errorResp.GetError())
	}

	devices := []Device{}
	for _, d := range gdr {
		devices = append(devices, Device{
			id:     d.ID,
			kind:   d.DeviceInfo.Kind,
			name:   d.DeviceInfo.Name,
			region: d.MgtInfo.Region,
			status: d.Meta.EnrollmentStatus,
			tenant: t,
			fqdn:   d.MgtInfo.Fqdn,
		})
	}

	return devices, err
}

// GetDevices gets a list of devices registered for the tenant
func (t *Tenant) GetDevices() ([]Device, error) {
	v, ok := t.app.deviceMap.Load(t.ID())
	if !ok || v == nil {
		return nil, errors.New("invalid tenant id")
	}
	deviceMap := v.(*sync.Map)
	devices := make([]Device, 0)
	deviceMap.Range(func(_, value interface{}) bool {
		device := value.(*Device)
		devices = append(devices, *device)
		return true
	})
	return devices, nil
}

func (t *Tenant) getDeviceByID(deviceId string) (*Device, error) {
	queryPath := path.Join(getDevicesPath, url.PathEscape(deviceId))

	var gdr getDeviceResponse
	var errorResp errorResponse

	response, err := t.httpClient.R().
		SetResult(&gdr).
		SetError(&errorResp).
		Get(queryPath)
	if err != nil {
		return nil, err
	}

	if response.IsError() {
		return nil, errors.New(errorResp.GetError())
	}

	return &Device{
		id:     gdr.ID,
		kind:   gdr.DeviceInfo.Kind,
		name:   gdr.DeviceInfo.Name,
		region: gdr.MgtInfo.Region,
		status: gdr.Meta.EnrollmentStatus,
		tenant: t,
		fqdn:   gdr.MgtInfo.Fqdn,
	}, nil
}

// GetDevice returns information for a specific device
func (t *Tenant) GetDevice(deviceId string) (*Device, error) {
	v, ok := t.app.deviceMap.Load(t.ID())
	if !ok || v == nil {
		return nil, errors.New("invalid deviceId")
	}
	deviceMapInternal := v.(*sync.Map)
	v, ok = deviceMapInternal.Load(deviceId)
	if !ok || v == nil {
		return nil, errors.New("invalid deviceId")
	}
	device := v.(*Device)
	return device, nil
}

func (t *Tenant) setHttpClient(httpClient *resty.Client) {
	t.httpClient = httpClient
	t.httpClient.OnBeforeRequest(func(_ *resty.Client, request *resty.Request) error {
		request.SetHeader("X-API-KEY", t.ApiToken())
		return nil
	})
}

func (t *Tenant) setRegionalHttpClients(regionalHttpClients map[string]*resty.Client) {
	t.regionalHttpClients = regionalHttpClients
	for regionalFQDN := range t.regionalHttpClients {
		t.regionalHttpClients[regionalFQDN].OnBeforeRequest(func(_ *resty.Client, request *resty.Request) error {
			request.SetHeader("X-API-KEY", t.ApiToken())
			return nil
		})
	}
}

func (t *Tenant) MarshalJSON() ([]byte, error) {
	tenant := make(map[string]interface{})
	tenant["id"] = t.ID()
	tenant["name"] = t.Name()
	tenant["apiToken"] = t.ApiToken()

	return json.Marshal(tenant)
}
