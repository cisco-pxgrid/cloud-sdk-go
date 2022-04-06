package cloud

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"github.com/cisco-pxgrid/cloud-sdk-go/internal/pubsub/test"
	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/suite"
)

var (
	apiPaths = struct {
		subscriptions string
		pubsub        string
	}{
		subscriptions: "/api/dxhub/v1/registry/subscriptions",
		pubsub:        "/api/v2/pubsub",
	}

	getDevicesJsonResponse = `[{
		"_id": "60936aa803877a672056da7b",
			"tId": "c4cc90f0-0eca-4aa0-9f20-7105d0ad3567",
			"deviceId": "60936aa803877a672056da7b",
			"clientId": "",
			"deviceInfo": {
			"deviceType": "cisco-ise",
				"name": "sjc-511-1",
				"description": "testing token",
				"scopes": [
	"IDM:tenant:Client"
	],
	"roles": [
	"SUPER-ADMIN"
	]
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
	}]`
)

type AppTestSuite struct {
	suite.Suite
	config Config
}

func (suite *AppTestSuite) SetupTest() {
	s := test.NewRPCServer(suite.T(), test.Config{
		PubSubPath:        apiPaths.pubsub,
		SubscriptionsPath: apiPaths.subscriptions,
	})
	u, _ := url.Parse(s.URL)

	credentials := Credentials{
		ApiKey: []byte("xyz"),
	}
	suite.config = Config{
		ID:            "appId",
		RegionalFQDN:  u.Host,
		GlobalFQDN:    u.Host,
		WriteStreamID: "writeStream",
		ReadStreamID:  "readStream",
		GetCredentials: func() (*Credentials, error) {
			return &credentials, nil
		},
		DeviceActivationHandler: func(device *Device) {
		},
		DeviceDeactivationHandler: func(device *Device) {
		},
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}
}

func (suite *AppTestSuite) TestNew() {
	app, err := New(suite.config)
	suite.Nil(err)
	suite.NotNil(app)
}

func (suite *AppTestSuite) TestLinkTenant() {
	jsonResponse := `{
    "api_token": "api_token",
    "tenant_id": "tenant_id",
    "tenant_name": "TNT000",
    "assigned_scopes": [
        "App:Scope:2"
    ],
    "attributes": {}
}`
	app, _ := New(suite.config)
	httpmock.ActivateNonDefault(app.httpClient.GetClient())
	defer httpmock.DeactivateAndReset()
	httpmock.RegisterResponder(
		http.MethodPost,
		redeemPath,
		func(_ *http.Request) (*http.Response, error) {
			resp := httpmock.NewStringResponse(http.StatusOK, jsonResponse)
			resp.Header.Set("Content-Type", "application/json")
			return resp, nil
		},
	)

	httpmock.
		RegisterResponder(
			http.MethodGet,
			getDevicesPath,
			func(_ *http.Request) (*http.Response, error) {
				resp := httpmock.NewStringResponse(http.StatusOK, getDevicesJsonResponse)
				resp.Header.Set("Content-Type", "application/json")
				return resp, nil
			})

	tenant, err := app.LinkTenant("otp_string")
	suite.Nil(err)
	suite.EqualValues("tenant_id", tenant.ID())
	suite.EqualValues("api_token", tenant.ApiToken())
	suite.EqualValues("TNT000", tenant.Name())
}

func (suite *AppTestSuite) TestUnlinkTenant() {
	app, _ := New(suite.config)
	tenant := Tenant{id: "tenant_id"}
	app.tenantMap = sync.Map{}
	app.tenantMap.Store(tenant.ID(), &tenant)
	unlinkUrl := fmt.Sprintf(unlinkPath, app.config.ID, tenant.ID())
	httpmock.ActivateNonDefault(app.httpClient.GetClient())
	defer httpmock.DeactivateAndReset()
	httpmock.RegisterResponder(
		http.MethodDelete,
		unlinkUrl,
		func(_ *http.Request) (*http.Response, error) {
			resp := httpmock.NewStringResponse(http.StatusNoContent, "")
			resp.Header.Set("Content-Type", "application/json")
			return resp, nil
		},
	)

	err := app.UnlinkTenant(&tenant)
	suite.Nil(err)
}

func (suite *AppTestSuite) TestUnlinkTenant_Error() {
	jsonResponse := `{"error": "some error"}`

	app, _ := New(suite.config)
	tenant := Tenant{id: "tenant_id"}
	app.tenantMap = sync.Map{}
	app.tenantMap.Store(tenant.ID(), &tenant)
	unlinkUrl := fmt.Sprintf(unlinkPath, app.config.ID, tenant.ID())
	httpmock.ActivateNonDefault(app.httpClient.GetClient())
	defer httpmock.DeactivateAndReset()
	httpmock.RegisterResponder(
		http.MethodDelete,
		unlinkUrl,
		func(_ *http.Request) (*http.Response, error) {
			resp := httpmock.NewStringResponse(http.StatusConflict, jsonResponse)
			resp.Header.Set("Content-Type", "application/json")
			return resp, nil
		},
	)

	err := app.UnlinkTenant(&tenant)
	suite.NotNil(err)
	suite.EqualValues("some error", err.Error())
}

func (suite *AppTestSuite) TestSetTenant() {
	app, _ := New(suite.config)
	httpmock.ActivateNonDefault(app.httpClient.GetClient())
	defer httpmock.DeactivateAndReset()

	httpmock.
		RegisterResponder(
			http.MethodGet,
			getDevicesPath,
			func(_ *http.Request) (*http.Response, error) {
				resp := httpmock.NewStringResponse(http.StatusOK, getDevicesJsonResponse)
				resp.Header.Set("Content-Type", "application/json")
				return resp, nil
			})

	_, err := app.SetTenant("tenant_id", "tenant_name", "api_token")
	suite.Nil(err)
}

func TestAppTestSuite(t *testing.T) {
	suite.Run(t, new(AppTestSuite))
}
