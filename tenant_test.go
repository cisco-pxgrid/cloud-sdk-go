package cloud

import (
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/go-resty/resty/v2"
	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/suite"
)

type TenantTestSuite struct {
	suite.Suite
}

func (suite *TenantTestSuite) TestGetDevices() {
	device1 := Device{id: "1"}
	device2 := Device{id: "2"}
	expected := []Device{
		device1,
		device2,
	}
	sort.Slice(expected, func(i, j int) bool {
		return expected[i].id < expected[j].id
	})

	app := &App{}
	tenant := Tenant{id: "tenantId"}
	tenant.app = app
	deviceMapInternal := sync.Map{}
	deviceMapInternal.Store(device1.id, &device1)
	deviceMapInternal.Store(device2.id, &device2)
	app.deviceMap.Store(tenant.ID(), &deviceMapInternal)

	devices, err := tenant.GetDevices()
	if err != nil {
		suite.Fail(err.Error())
	}
	sort.Slice(devices, func(i, j int) bool {
		return devices[i].id < devices[j].id
	})
	suite.T().Log("Expected:", expected, " Received: ", devices)
	suite.True(reflect.DeepEqual(expected, devices))
}

func (suite *TenantTestSuite) Test_getDevices() {
	jsonResponse := `[
		{
			"_id": "60936aa803877a672056da7b",
			"tId": "c4cc90f0-0eca-4aa0-9f20-7105d0ad3567",
			"deviceId": "60936aa803877a672056da7b",
			"clientId": "",
			"deviceInfo": {
				"deviceType": "cisco-ise",
				"name": "sjc-511-1",
				"description": "testing token",
				"scopes": ["IDM:tenant:Client"],
				"roles": ["SUPER-ADMIN"]
			},
			"mgtInfo": {
				"region": "us-west-2",
				"fqdn": "int-mm.tesseractinternal.com",
				"statusesUrl": "https://int-mm.tesseractinternal.com/api/uno/v1/devicemgr/60936aa803877a672056da7b/statuses",
				"syncsUrl": "https://int-mm.tesseractinternal.com/api/uno/v1/devicemgr/60936aa803877a672056da7b/syncs",
				"paramsUrl": "https://int-mm.tesseractinternal.com/api/uno/v1/devicemgr/60936aa803877a672056da7b/params",
				"capabilitiesUrl": "https://int-mm.tesseractinternal.com/api/uno/v1/devicemgr/60936aa803877a672056da7b/capabilities"
			},
			"meta": {
				"created": "2021-05-06 04:03:52.297685672 +0000 UTC m=+1712407.109293529",
				"lastModified": "2021-05-07 12:32:19.01815824 +0000 UTC m=+1829313.829766099",
				"enrollmentStatus": "un-enrolled"
			}
		}
	]`
	tenant := Tenant{}
	expected := []Device{
		{
			id:     "60936aa803877a672056da7b",
			kind:   "cisco-ise",
			name:   "sjc-511-1",
			region: "us-west-2",
			status: "un-enrolled",
			tenant: &tenant,
		},
	}

	httpClient := resty.New().SetBaseURL("https://test.com")
	tenant.setHttpClient(httpClient)
	httpmock.ActivateNonDefault(httpClient.GetClient())
	defer httpmock.DeactivateAndReset()
	httpmock.
		RegisterResponder(
			http.MethodGet,
			getDevicesPath,
			func(_ *http.Request) (*http.Response, error) {
				resp := httpmock.NewStringResponse(http.StatusOK, jsonResponse)
				resp.Header.Set("Content-Type", "application/json")
				return resp, nil
			})

	devices, err := tenant.getDevices()
	if err != nil {
		suite.Fail(err.Error())
	}
	suite.EqualValues(expected, devices)
}

func (suite *TenantTestSuite) Test_getDeviceByID() {
	jsonResponse := `{
		"_id": "60936aa803877a672056da7b",
		"tId": "c4cc90f0-0eca-4aa0-9f20-7105d0ad3567",
		"deviceId": "60936aa803877a672056da7b",
		"clientId": "",
		"deviceInfo": {
			"deviceType": "cisco-ise",
			"name": "sjc-511-1",
			"description": "testing token",
			"scopes": ["IDM:tenant:Client"],
			"roles": ["SUPER-ADMIN"]
		},
		"mgtInfo": {
			"region": "us-west-2",
			"fqdn": "int-mm.tesseractinternal.com",
			"statusesUrl": "https://int-mm.tesseractinternal.com/api/uno/v1/devicemgr/60936aa803877a672056da7b/statuses",
			"syncsUrl": "https://int-mm.tesseractinternal.com/api/uno/v1/devicemgr/60936aa803877a672056da7b/syncs",
			"paramsUrl": "https://int-mm.tesseractinternal.com/api/uno/v1/devicemgr/60936aa803877a672056da7b/params",
			"capabilitiesUrl": "https://int-mm.tesseractinternal.com/api/uno/v1/devicemgr/60936aa803877a672056da7b/capabilities"
		},
		"meta": {
			"created": "2021-05-06 04:03:52.297685672 +0000 UTC m=+1712407.109293529",
			"lastModified": "2021-05-07 12:32:19.01815824 +0000 UTC m=+1829313.829766099",
			"enrollmentStatus": "un-enrolled"
		}
	}`

	tenant := Tenant{}
	expected := Device{
		id:     "60936aa803877a672056da7b",
		kind:   "cisco-ise",
		name:   "sjc-511-1",
		region: "us-west-2",
		status: "un-enrolled",
		tenant: &tenant,
	}
	httpClient := resty.New().SetBaseURL("https://test.com")
	tenant.setHttpClient(httpClient)
	httpmock.ActivateNonDefault(httpClient.GetClient())
	defer httpmock.DeactivateAndReset()
	httpmock.
		RegisterResponder(
			http.MethodGet,
			fmt.Sprintf(getDevicesPath+"/%s", "any_id"),
			func(_ *http.Request) (*http.Response, error) {
				resp := httpmock.NewStringResponse(http.StatusOK, jsonResponse)
				resp.Header.Set("Content-Type", "application/json")
				return resp, nil
			})

	device, err := tenant.getDeviceByID("any_id")
	if err != nil {
		suite.Fail(err.Error())
	}
	suite.EqualValues(&expected, device)
}

func (suite *TenantTestSuite) TestGetDeviceByID() {
	device1 := Device{id: "1"}
	device2 := Device{id: "2"}

	app := &App{}
	tenant := Tenant{id: "tenantId"}
	tenant.app = app
	deviceMapInternal := sync.Map{}
	deviceMapInternal.Store(device1.ID(), &device1)
	deviceMapInternal.Store(device2.ID(), &device2)
	app.deviceMap.Store(tenant.ID(), &deviceMapInternal)

	device, err := tenant.GetDevice("2")
	if err != nil {
		suite.Fail(err.Error())
	}
	suite.EqualValues(&device2, device)
}

func TestTenantTestSuite(t *testing.T) {
	suite.Run(t, new(TenantTestSuite))
}
