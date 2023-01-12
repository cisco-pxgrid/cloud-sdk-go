package cloud

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type envelop struct {
	Method    string              `json:"method"`
	Url       string              `json:"url"`
	Headers   map[string][]string `json:"headers"`
	ObjectUrl string              `json:"objectUrl,omitempty"`
	Body      string              `json:"body,omitempty"`
}

type createResponse struct {
	ObjectUrl string `json:"objectUrl"`
}

type queryResponse struct {
	Id        string              `json:"queryId"`
	Status    string              `json:"status"`
	Progress  string              `json:"progress,omitempty"`
	Code      int                 `json:"code"`
	Headers   map[string][]string `json:"headers,omitempty"`
	ObjectUrl string              `json:"objectUrl,omitempty"`
	Body      string              `json:"body,omitempty"`
}

var (
	RequestBodyMax    = 300000
	StatusPollTimeMin = 500 * time.Millisecond
	StatusPollTimeMax = 15 * time.Second
)

const (
	RequestIdleTimeout = 60 * time.Second
	RequestTimeout     = 10 * time.Minute
)

// Query for pxGrid, ERS or other API
// Hostname, authentication will be filled by the SDK
// Underlying direct mode with API-Proxy
// Context, URL, headers, body...etc can be set within request
func (d *Device) oldQuery(request *http.Request) (*http.Response, error) {
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

func (d *Device) Query(request *http.Request) (*http.Response, error) {
	reqEnv := envelop{
		Method:  request.Method,
		Url:     request.RequestURI,
		Headers: request.Header,
	}
	// TODO check if this is too big
	payload := make([]byte, RequestBodyMax)
	payloadLength := 0
	if request.Body != nil {
		reader := request.Body
		var err error
		payloadLength, err = reader.Read(payload)
		if err != nil {
			return nil, err
		}
	}
	if payloadLength == RequestBodyMax {
		// Create req object
		createEnv := createResponse{}
		queryPath := fmt.Sprintf(directModePath, url.PathEscape(d.ID()), "/query/object")
		req := d.tenant.regionalHttpClient.R()
		req.SetHeader("X-API-PROXY-COMMUNICATION-STYLE", "sync")
		req.SetResult(&createEnv)
		resp, err := req.Execute(http.MethodPost, queryPath)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode() != http.StatusOK {
			return nil, fmt.Errorf("request error %s", resp.Status())
		}
		reqEnv.ObjectUrl = createEnv.ObjectUrl

		// Upload already read b and remaining body
		reader := io.MultiReader(bytes.NewReader(payload), request.Body)
		hresp, err := http.Post(createEnv.ObjectUrl, "", reader)
		if err != nil {
			return nil, err
		}
		if hresp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("request error %s", hresp.Status)
		}
	} else {
		reqEnv.Body = base64.StdEncoding.EncodeToString(payload[0:payloadLength])
	}

	// Trigger query
	respEnv := queryResponse{}
	queryPath := fmt.Sprintf(directModePath, url.PathEscape(d.ID()), "/query")
	req := d.tenant.regionalHttpClient.R()
	req.SetHeader("X-API-PROXY-COMMUNICATION-STYLE", "sync")
	req.SetBody(reqEnv)
	req.SetResult(&respEnv)
	resp, err := req.Execute(http.MethodPost, queryPath)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode() == http.StatusNotFound {
		// fallback only if small payload
		if payloadLength < RequestBodyMax {
			return d.fallbackQuery(request, payload)
		}
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("request error %s", resp.Status())
	}

	// Poll query status
	queryId := respEnv.Id
	progress := respEnv.Progress
	pollDuration := StatusPollTimeMin
	time.Sleep(pollDuration)
	requestTimeoutCh := time.After(RequestTimeout)
	requestIdleTimeoutCh := time.After(RequestIdleTimeout)
	for respEnv.Status == "RUNNING" {
		queryPath := fmt.Sprintf(directModePath, url.PathEscape(d.ID()), "/query/"+queryId)
		req := d.tenant.regionalHttpClient.R()
		req.SetHeader("X-API-PROXY-COMMUNICATION-STYLE", "sync")
		req.SetBody(respEnv)
		req.SetResult(&respEnv)
		resp, err = req.Execute(http.MethodGet, queryPath)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode() != http.StatusOK {
			return nil, fmt.Errorf("request error %s", resp.Status())
		}
		if progress < respEnv.Progress {
			// reset idle timeout
			progress = respEnv.Progress
			requestIdleTimeoutCh = time.After(RequestIdleTimeout)
		}
		if pollDuration < StatusPollTimeMax {
			pollDuration *= 2
			if pollDuration > StatusPollTimeMax {
				pollDuration = StatusPollTimeMax
			}
		}
		select {
		case <-req.Context().Done():
			return nil, nil
		case <-requestIdleTimeoutCh:
			return nil, fmt.Errorf("request idle timeout")
		case <-requestTimeoutCh:
			return nil, fmt.Errorf("request timeout")
		case <-time.After(pollDuration):
		}
	}

	// Check body or object in response
	var response *http.Response
	if respEnv.ObjectUrl != "" {
		// Download object
		response, err = http.Get(respEnv.ObjectUrl)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode() != http.StatusOK {
			return nil, fmt.Errorf("request error %s", resp.Status())
		}
		response.Body = newQueryCloser(d, queryId, response.Body)
	} else {
		raw, err := base64.StdEncoding.DecodeString(respEnv.Body)
		if err != nil {
			return nil, err
		}
		// Direct body response
		response = &http.Response{
			StatusCode: respEnv.Code,
			Status:     http.StatusText(respEnv.Code),
			Body:       newQueryCloser(d, queryId, io.NopCloser(bytes.NewReader(raw))),
		}
	}
	// Replace with actual headers
	response.Header = respEnv.Headers
	return response, nil
}

func (d *Device) fallbackQuery(request *http.Request, payload []byte) (*http.Response, error) {
	queryPath := fmt.Sprintf(directModePath, url.PathEscape(d.ID()), request.URL)

	req := d.tenant.regionalHttpClient.R()

	if request.Header != nil {
		for name, values := range request.Header {
			req.SetHeader(name, values[0])
		}
	}
	req.SetHeader("X-API-PROXY-COMMUNICATION-STYLE", "sync")
	req.SetBody(payload)
	req.SetDoNotParseResponse(true)

	response, err := req.Execute(request.Method, queryPath)

	return response.RawResponse, err
}

func newQueryCloser(device *Device, queryId string, reader io.ReadCloser) io.ReadCloser {
	return &queryCloser{device: device, queryId: queryId, reader: reader}
}

type queryCloser struct {
	device  *Device
	queryId string
	reader  io.ReadCloser
}

func (q *queryCloser) Read(p []byte) (n int, err error) {
	return q.reader.Read(p)
}

func (q *queryCloser) Close() error {
	// Ignore close error and continue
	q.reader.Close()

	queryPath := fmt.Sprintf(directModePath, url.PathEscape(q.device.ID()), "/query/"+q.queryId)
	req := q.device.tenant.regionalHttpClient.R()
	req.SetHeader("X-API-PROXY-COMMUNICATION-STYLE", "sync")
	_, err := req.Execute(http.MethodDelete, queryPath)
	return err
}
