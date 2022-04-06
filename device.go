package cloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
)

// Device represents an ISE deployment that's registered with pxGrid Cloud
type Device struct {
	id     string
	kind   string
	name   string
	region string
	status string
	tenant *Tenant
}

func (d *Device) String() string {
	return fmt.Sprintf("Device[ID: %s, Name: %s, Type: %s: Tenant: %s]",
		d.id, d.name, d.kind, d.tenant.id)
}

// ID returns device's id
func (d *Device) ID() string {
	return d.id
}

// Name returns device's name
func (d *Device) Name() string {
	return d.name
}

// Type returns device's type
func (d *Device) Type() string {
	return d.kind
}

// Region returns device's region
func (d *Device) Region() string {
	return d.region
}

// Tenant returns device's tenant
func (d *Device) Tenant() *Tenant {
	return d.tenant
}

// DeviceStatus represents the status of a device
type DeviceStatus struct {
	Status string
}

type getDeviceResponse struct {
	ID         string `json:"deviceId"`
	DeviceInfo struct {
		Kind string `json:"deviceType"`
		Name string `json:"name"`
	} `json:"deviceInfo"`
	MgtInfo struct {
		Region string `json:"region"`
	} `json:"mgtInfo"`
	Meta struct {
		EnrollmentStatus string `json:"enrollmentStatus"`
	} `json:"meta"`
}

// Status fetches actual status of the device
func (d *Device) Status() (*DeviceStatus, error) {
	queryPath := fmt.Sprintf(path.Join(getDevicesPath, "/%s"), url.PathEscape(d.id))

	var device getDeviceResponse
	var errorResp errorResponse

	response, err := d.tenant.httpClient.R().
		SetResult(&device).
		SetError(&errorResp).
		Get(queryPath)
	if err != nil {
		return nil, err
	}

	if response.IsError() {
		return nil, errors.New(errorResp.GetError())
	}

	resp := &DeviceStatus{
		Status: device.Meta.EnrollmentStatus,
	}

	return resp, err
}

func (d *Device) MarshalJSON() ([]byte, error) {
	device := make(map[string]interface{})
	device["id"] = d.ID()
	device["kind"] = d.Type()
	device["name"] = d.Name()
	device["region"] = d.Region()
	device["status"] = d.status
	device["tenant"] = d.tenant.ID()

	return json.Marshal(device)
}
