package cloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sync"

	"github.com/cisco-pxgrid/cloud-sdk-go/log"
	"github.com/go-resty/resty/v2"
)

// NOTE: this build of the SDK unconditionally prints raw credentials
// (tenant API tokens, app API keys, X-Api-Key / X-API-KEY / Authorization
// headers) in DEBUG output. It is intended for the single connector pod
// used for platform-team triage. DO NOT ship to production.

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

// httpDebugBodyMax caps how many bytes of a request/response body are
// rendered in debug logs. Tune as needed; tenant device-list payloads are
// generally small.
const httpDebugBodyMax = 4096

// truncate trims b to at most n bytes and appends an ellipsis marker if
// the original was longer. Returned as a string for logging.
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + fmt.Sprintf("…(%d more bytes)", len(b)-n)
}

// sanitizeHeaders returns a copy of h. In this triage build the raw
// credential-bearing header values (Authorization, X-Api-Key, X-Api-Token,
// Cookie) ARE included verbatim — see the package-level note above.
func sanitizeHeaders(h http.Header) map[string][]string {
	if h == nil {
		return nil
	}
	out := make(map[string][]string, len(h))
	for k, v := range h {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// logHTTPCall emits a uniformly-formatted DEBUG line for every platform
// HTTP call made by the SDK. label disambiguates the call site
// (e.g. "tenant.getDevices"). It is safe to pass a nil response when err
// fired before resty produced one.
func logHTTPCall(label string, req *resty.Request, resp *resty.Response, err error) {
	if req == nil {
		log.Logger.Debugf("%s: <nil request> err=%v", label, err)
		return
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	fullURL := req.URL
	if resp != nil && resp.Request != nil && resp.Request.RawRequest != nil && resp.Request.RawRequest.URL != nil {
		fullURL = resp.Request.RawRequest.URL.String()
	}
	reqHeaders := sanitizeHeaders(req.Header)
	if err != nil {
		log.Logger.Debugf("%s: HTTP %s %s headers=%v transport-error=%v",
			label, method, fullURL, reqHeaders, err)
		return
	}
	if resp == nil {
		log.Logger.Debugf("%s: HTTP %s %s headers=%v <no response>", label, method, fullURL, reqHeaders)
		return
	}
	body := resp.Body()
	log.Logger.Debugf("%s: HTTP %s %s status=%d duration=%s reqHeaders=%v respHeaders=%v respBytes=%d respBody=%s",
		label, method, fullURL, resp.StatusCode(), resp.Time(), reqHeaders, resp.Header(), len(body), truncate(body, httpDebugBodyMax))
}

func (t *Tenant) getDevices() ([]Device, error) {
	var gdr []getDeviceResponse
	var errorResp errorResponse

	tenantID := "<unknown>"
	tenantToken := ""
	if t != nil {
		tenantID = t.id
		tenantToken = t.apiToken
	}
	log.Logger.Debugf("tenant.getDevices: enter tenant.id=%s tenant.apiToken=%s path=%s [RAW CREDENTIALS IN LOG]", tenantID, tenantToken, getDevicesPath)

	req := t.httpClient.R().
		SetResult(&gdr).
		SetError(&errorResp)
	response, err := req.Get(getDevicesPath)
	logHTTPCall("tenant.getDevices", req, response, err)
	if err != nil {
		return nil, err
	}

	if response.IsError() {
		log.Logger.Debugf("tenant.getDevices: tenant.id=%s platform returned error status=%d body=%s",
			tenantID, response.StatusCode(), truncate(response.Body(), httpDebugBodyMax))
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
	log.Logger.Debugf("tenant.getDevices: tenant.id=%s parsed count=%d", tenantID, len(devices))
	for i := range devices {
		log.Logger.Debugf("tenant.getDevices:   [%d] id=%s name=%q type=%s region=%s fqdn=%s status=%s",
			i, devices[i].id, devices[i].name, devices[i].kind, devices[i].region, devices[i].fqdn, devices[i].status)
	}

	return devices, err
}

// GetDevices gets a list of devices registered for the tenant
func (t *Tenant) GetDevices() ([]Device, error) {
	v, ok := t.app.deviceMap.Load(t.ID())
	if !ok || v == nil {
		log.Logger.Debugf("Tenant.GetDevices: tenant.id=%s not found in app.deviceMap (no SetTenant/LinkTenant has populated it yet)", t.ID())
		return nil, errors.New("invalid tenant id")
	}
	deviceMap := v.(*sync.Map)
	devices := make([]Device, 0)
	deviceMap.Range(func(_, value interface{}) bool {
		device := value.(*Device)
		devices = append(devices, *device)
		return true
	})
	log.Logger.Debugf("Tenant.GetDevices: tenant.id=%s returning %d device(s) from local cache (this is NOT a platform call — cache is populated at LinkTenant/SetTenant and on every device:activate)", t.ID(), len(devices))
	for i := range devices {
		log.Logger.Debugf("Tenant.GetDevices:   [%d] id=%s name=%q region=%s fqdn=%s status=%s",
			i, devices[i].id, devices[i].name, devices[i].region, devices[i].fqdn, devices[i].status)
	}
	return devices, nil
}

func (t *Tenant) getDeviceByID(deviceId string) (*Device, error) {
	queryPath := path.Join(getDevicesPath, url.PathEscape(deviceId))

	var gdr getDeviceResponse
	var errorResp errorResponse

	tenantID := "<unknown>"
	tenantToken := ""
	if t != nil {
		tenantID = t.id
		tenantToken = t.apiToken
	}
	log.Logger.Debugf("tenant.getDeviceByID: enter tenant.id=%s tenant.apiToken=%s deviceId=%s path=%s [RAW CREDENTIALS IN LOG]", tenantID, tenantToken, deviceId, queryPath)

	req := t.httpClient.R().
		SetResult(&gdr).
		SetError(&errorResp)
	response, err := req.Get(queryPath)
	logHTTPCall("tenant.getDeviceByID", req, response, err)
	if err != nil {
		return nil, err
	}

	if response.IsError() {
		log.Logger.Debugf("tenant.getDeviceByID: tenant.id=%s deviceId=%s platform error status=%d body=%s",
			tenantID, deviceId, response.StatusCode(), truncate(response.Body(), httpDebugBodyMax))
		return nil, errors.New(errorResp.GetError())
	}

	dev := &Device{
		id:     gdr.ID,
		kind:   gdr.DeviceInfo.Kind,
		name:   gdr.DeviceInfo.Name,
		region: gdr.MgtInfo.Region,
		status: gdr.Meta.EnrollmentStatus,
		tenant: t,
		fqdn:   gdr.MgtInfo.Fqdn,
	}
	log.Logger.Debugf("tenant.getDeviceByID: tenant.id=%s parsed id=%s name=%q type=%s region=%s fqdn=%s status=%s",
		tenantID, dev.id, dev.name, dev.kind, dev.region, dev.fqdn, dev.status)
	return dev, nil
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
	for _, regionalHttpClient := range t.regionalHttpClients {
		regionalHttpClient.OnBeforeRequest(func(_ *resty.Client, request *resty.Request) error {
			request.SetHeader("X-API-KEY", t.ApiToken())
			return nil
		})
	}
}

// DeviceInfo contains the regional endpoint information for a device.
// This is the subset of Device information needed by a consumer that only
// holds a tenant API token (e.g. MPE / Common Services) and needs to
// discover which regional pxGrid Cloud endpoint to use for ERS API calls.
type DeviceInfo struct {
	// ID is the unique device identifier
	ID string
	// Name is the human-readable device name
	Name string
	// Region is the cloud region where the device is enrolled
	Region string
	// Fqdn is the regional pxGrid Cloud endpoint for this device.
	// Use this as the base URL for ERS / pxGrid API calls via the API proxy.
	Fqdn string
	// Status is the device enrollment status
	Status string
}

// GetDevicesForTenant discovers all devices registered for a tenant and returns
// their regional endpoint FQDNs. It requires only the global FQDN and the
// tenant API token obtained from OTP redemption — no app API key is needed.
//
// This is the entry point for consumers such as MPE or Common Services that
// hold a long-lived tenant API token and need to determine the correct
// regional pxGrid Cloud endpoint before making ERS / pxGrid API calls.
//
// The transport parameter may be nil, in which case a default TLS transport is used.
func GetDevicesForTenant(globalFQDN, tenantAPIToken string, transport http.RoundTripper) ([]DeviceInfo, error) {
	if globalFQDN == "" {
		return nil, errors.New("globalFQDN must not be empty")
	}
	if tenantAPIToken == "" {
		return nil, errors.New("tenantAPIToken must not be empty")
	}

	hostURL := url.URL{
		Scheme: defaultHTTPScheme,
		Path:   url.PathEscape(globalFQDN),
	}

	client := resty.New().SetBaseURL(hostURL.String())
	if transport != nil {
		client.SetTransport(transport)
	}
	client.OnBeforeRequest(func(_ *resty.Client, r *resty.Request) error {
		r.SetHeader("X-API-KEY", tenantAPIToken)
		return nil
	})

	var gdr []getDeviceResponse
	var errResp errorResponse
	log.Logger.Debugf("GetDevicesForTenant: enter globalFQDN=%s tenantAPIToken=%s path=%s [RAW CREDENTIALS IN LOG]", globalFQDN, tenantAPIToken, getDevicesPath)
	req := client.R().
		SetResult(&gdr).
		SetError(&errResp)
	resp, err := req.Get(getDevicesPath)
	logHTTPCall("GetDevicesForTenant", req, resp, err)
	if err != nil {
		return nil, fmt.Errorf("failed to get devices: %w", err)
	}
	if resp.IsError() {
		log.Logger.Debugf("GetDevicesForTenant: platform error status=%d body=%s",
			resp.StatusCode(), truncate(resp.Body(), httpDebugBodyMax))
		return nil, fmt.Errorf("failed to get devices: %s", errResp.GetError())
	}

	devices := make([]DeviceInfo, 0, len(gdr))
	for _, d := range gdr {
		devices = append(devices, DeviceInfo{
			ID:     d.ID,
			Name:   d.DeviceInfo.Name,
			Region: d.MgtInfo.Region,
			Fqdn:   d.MgtInfo.Fqdn,
			Status: d.Meta.EnrollmentStatus,
		})
	}
	log.Logger.Debugf("GetDevicesForTenant: parsed count=%d", len(devices))
	return devices, nil
}

func (t *Tenant) MarshalJSON() ([]byte, error) {
	tenant := make(map[string]interface{})
	tenant["id"] = t.ID()
	tenant["name"] = t.Name()
	tenant["apiToken"] = t.ApiToken()

	return json.Marshal(tenant)
}
