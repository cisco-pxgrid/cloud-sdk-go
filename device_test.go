package cloud

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/go-resty/resty/v2"
	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/suite"
)

type DeviceTestSuite struct {
	suite.Suite
}

func (suite *DeviceTestSuite) TestGetDeviceStatus() {
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
	httpClient := resty.New().SetHostURL("https://test.com")
	tenant.setHttpClient(httpClient)
	device := Device{id: "deviceId"}
	device.tenant = &tenant
	httpmock.ActivateNonDefault(httpClient.GetClient())
	defer httpmock.DeactivateAndReset()
	httpmock.RegisterResponder(
		http.MethodGet,
		fmt.Sprintf(getDevicesPath+"/%s", device.ID()),
		func(_ *http.Request) (*http.Response, error) {
			resp := httpmock.NewStringResponse(http.StatusOK, jsonResponse)
			resp.Header.Set("Content-Type", "application/json")
			return resp, nil
		},
	)
	status, err := device.Status()
	if err != nil {
		panic(err)
	}

	expected := &DeviceStatus{
		Status: "un-enrolled",
	}
	suite.EqualValues(expected, status)
}

func TestDeviceTestSuite(t *testing.T) {
	suite.Run(t, new(DeviceTestSuite))
}
