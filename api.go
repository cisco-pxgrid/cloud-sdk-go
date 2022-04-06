package cloud

import (
	"fmt"
	"net/http"
	"net/url"
)

// Query for pxGrid, ERS or other API
// Hostname, authentication will be filled by the SDK
// Underlying direct mode with API-Proxy
// Context, URL, headers, body...etc can be set within request
func (d *Device) Query(request *http.Request) (*http.Response, error) {
	queryPath := fmt.Sprintf(directModePath, url.PathEscape(d.ID()), request.URL)

	req := d.tenant.regionalHttpClient.R()

	if request.Header != nil {
		for name, values := range request.Header {
			req.SetHeader(name, values[0])
		}
	}
	req.SetHeader("X-API-PROXY-COMMUNICATION-STYLE", "sync")

	req.SetBody(request.Body)
	req.SetDoNotParseResponse(true)

	response, err := req.Execute(request.Method, queryPath)

	return response.RawResponse, err
}
