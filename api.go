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
	Code      int                 `json:"code,omitempty"`
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
func (d *Device) Query(request *http.Request) (*http.Response, error) {
	reqEnv := envelop{
		Method:  request.Method,
		Url:     request.URL.String(),
		Headers: request.Header,
	}
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
		// Payload more than max
		// Create request object
		createEnv := createResponse{}
		queryPath := fmt.Sprintf(directModePath, url.PathEscape(d.ID()), "/query/object")
		resp, err := d.tenant.regionalHttpClient.R().
			SetHeader("X-API-PROXY-COMMUNICATION-STYLE", "sync").
			SetResult(&createEnv).
			Post(queryPath)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode() == http.StatusNotFound {
			// Large API payload not supported by this device
			return nil, fmt.Errorf("payload too large for this device")
		}
		if resp.StatusCode() != http.StatusOK {
			return nil, fmt.Errorf("failed to create object: %s", resp.Status())
		}
		reqEnv.ObjectUrl = createEnv.ObjectUrl

		// Upload already read payload and remaining body
		reader := io.MultiReader(bytes.NewReader(payload), request.Body)
		hresp, err := d.tenant.regionalHttpClient.R().
			SetBody(reader).
			SetDoNotParseResponse(true).
			Post(createEnv.ObjectUrl)
		if err != nil {
			return nil, err
		}
		if hresp.StatusCode() != http.StatusOK {
			return nil, fmt.Errorf("failed to upload request object: %s", hresp.Status())
		}
	} else {
		// Request size does not require ObjectStore
		reqEnv.Body = base64.StdEncoding.EncodeToString(payload[0:payloadLength])
	}

	// Trigger query
	respEnv := queryResponse{}
	queryPath := fmt.Sprintf(directModePath, url.PathEscape(d.ID()), "/query")
	resp, err := d.tenant.regionalHttpClient.R().
		SetHeader("X-API-PROXY-COMMUNICATION-STYLE", "sync").
		SetBody(reqEnv).
		SetResult(&respEnv).
		Post(queryPath)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode() == http.StatusNotFound {
		// Fallback only if small payload
		if payloadLength < RequestBodyMax {
			return d.fallbackQuery(request, payload[0:payloadLength])
		}
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("failed to query: %s", resp.Status())
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
		resp, err = d.tenant.regionalHttpClient.R().
			SetHeader("X-API-PROXY-COMMUNICATION-STYLE", "sync").
			SetBody(respEnv).
			SetResult(&respEnv).
			Get(queryPath)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode() != http.StatusOK {
			return nil, fmt.Errorf("request error %s", resp.Status())
		}
		if respEnv.Status != "RUNNING" {
			break
		}
		if progress != respEnv.Progress {
			// Reset idle timeout
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
		case <-request.Context().Done():
			return nil, nil
		case <-requestIdleTimeoutCh:
			return nil, fmt.Errorf("request idle timeout")
		case <-requestTimeoutCh:
			return nil, fmt.Errorf("request timeout")
		case <-time.After(pollDuration):
		}
	}

	// Check body or object in response
	var reader io.ReadCloser
	if respEnv.ObjectUrl != "" {
		// Download object
		hresp, err := d.tenant.regionalHttpClient.R().
			SetDoNotParseResponse(true).
			Get(respEnv.ObjectUrl)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode() != http.StatusOK {
			return nil, fmt.Errorf("request error %s", resp.Status())
		}
		reader = hresp.RawBody()
	} else {
		// Direct body response
		raw, err := base64.StdEncoding.DecodeString(respEnv.Body)
		if err != nil {
			return nil, err
		}
		reader = io.NopCloser(bytes.NewReader(raw))
	}
	response := &http.Response{
		StatusCode: respEnv.Code,
		Header:     respEnv.Headers,
		Status:     http.StatusText(respEnv.Code),
		Body:       newQueryCloser(d, queryId, reader),
	}
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
	req.SetHeader("X-Api-Proxy-Communication-Style", "sync")
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
	req.SetHeader("X-Api-Proxy-Communication-Style", "sync")
	_, err := req.Execute(http.MethodDelete, queryPath)
	return err
}
