package cloud

import (
	"fmt"
	"io"
	"net/http"

	"github.com/go-resty/resty/v2"
	"github.com/jarcoal/httpmock"
)

func (suite *DeviceTestSuite) TestQuery() {
	tenant := Tenant{}
	httpClient := resty.New().SetBaseURL("https://test.com")
	tenant.setRegionalHttpClient(httpClient)
	device := Device{id: "deviceId"}
	device.tenant = &tenant
	httpmock.ActivateNonDefault(httpClient.GetClient())
	defer httpmock.DeactivateAndReset()
	requestURI := "/just/some/path"
	httpmock.RegisterResponder(
		http.MethodGet,
		fmt.Sprintf(directModePath, device.ID(), requestURI),
		httpmock.NewStringResponder(http.StatusOK, "test_body"),
	)
	request, err := http.NewRequest(http.MethodGet, requestURI, nil)
	if err != nil {
		panic(err)
	}
	request.Header.Set("test", "test")

	response, err := device.Query(request)
	if err != nil {
		panic(err)
	}
	suite.EqualValues(http.StatusOK, response.StatusCode)

	body, err := io.ReadAll(response.Body)
	if err != nil {
		panic(err)
	}
	suite.EqualValues("test_body", string(body))
}
