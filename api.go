package cloud

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
)

type envelop struct {
	Method    string              `json:"method"`
	Url       string              `json:"url"`
	Headers   map[string][]string `json:"headers"`
	ObjectUrl string              `json:"objectUrl,omitempty"`
	Body      string              `json:"body,omitempty"`
	QueryID   string              `json:"queryId,omitempty"`
}

type createResponse struct {
	ObjectUrl string `json:"objectUrl"`
}

type createMultipartResponse struct {
	ObjectUrls []string `json:"objectUrls"`
	QueryID    string   `json:"queryId"`
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
	RequestBodyMax   = 300 * 1024
	RequestObjectMax = 100 * 1024 * 1024
	//2GB
	MultipartRequestObjectMax = 2 * 1024 * 1024 * 1024
	//50MB
	PartSize          = 50 * 1024 * 1024
	StatusPollTimeMin = 500 * time.Millisecond
	StatusPollTimeMax = 15 * time.Second
)

const (
	X_API_PROXY_COMMUNICATION_SYTLE = "X-Api-Proxy-Communication-Style"
	clientRegionError               = "Failed to find the client for region"
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
	// Read request
	payload := make([]byte, RequestBodyMax)
	payloadLength := 0
	if request.Body != nil {
		reader := request.Body
		var err error
		payloadLength, err = reader.Read(payload)
		if err != nil && err != io.EOF {
			return nil, err
		}
	}
	if payloadLength == RequestBodyMax {
		// Payload more than max, create request object
		createEnv := createResponse{}
		createMultipartEnv := createMultipartResponse{}
		queryPath := fmt.Sprintf(directModePath, url.PathEscape(d.ID()), "/query/object/multipart")

		regionalClient := d.tenant.regionalHttpClients[d.Fqdn()]
		if regionalClient == nil {
			return nil, fmt.Errorf("%s - %s", clientRegionError, d.Fqdn())
		}

		resp, err := regionalClient.R().
			SetHeader(X_API_PROXY_COMMUNICATION_SYTLE, "sync").
			SetResult(&createMultipartEnv).
			Post(queryPath)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode() == http.StatusNotFound {
			// Multipart Large API payload is not supported by this device, try single part
			queryPath := fmt.Sprintf(directModePath, url.PathEscape(d.ID()), "/query/object")

			regionalClient := d.tenant.regionalHttpClients[d.Fqdn()]
			if regionalClient == nil {
				return nil, fmt.Errorf("%s - %s", clientRegionError, d.Fqdn())
			}

			resp, err = regionalClient.R().
				SetHeader(X_API_PROXY_COMMUNICATION_SYTLE, "sync").
				SetResult(&createEnv).
				Post(queryPath)
			if err != nil {
				return nil, err
			}
			if resp.StatusCode() == http.StatusNotFound {
				// Large API payload is not supported by this device
				return nil, fmt.Errorf("payload too large for this device")
			}
		}
		if resp.StatusCode() != http.StatusOK {
			return nil, fmt.Errorf("failed to create object: %s", resp.Status())
		}

		// Upload previously read payload and remaining body
		reader := io.MultiReader(bytes.NewReader(payload), request.Body)
		if createMultipartEnv.QueryID != "" {
			reqEnv.QueryID = createMultipartEnv.QueryID

			// Read one more byte to check if max is crossed
			payloadTotalLength := 0
			var wg sync.WaitGroup
			var uploadErr error
			for i := 0; ; i++ {
				b, err := io.ReadAll(io.LimitReader(reader, int64(PartSize)))
				//Process bytes if err is EOF and return err only for other type of errors.
				if err != nil && err != io.EOF {
					return nil, err
				}
				if len(b) == 0 {
					break
				}
				payloadTotalLength += len(b)
				if payloadTotalLength > MultipartRequestObjectMax {
					return nil, fmt.Errorf("payload too large for this device")
				}

				wg.Add(1)
				go func(urlNumber int) {
					defer wg.Done()
					hresp, err := resty.New().SetTransport(d.Tenant().app.config.Transport).
						R().
						SetBody(b).
						SetDoNotParseResponse(true).
						Put(createMultipartEnv.ObjectUrls[urlNumber])
					if err != nil && uploadErr == nil {
						uploadErr = err
						return
					}
					if hresp.StatusCode() != http.StatusOK {
						if uploadErr == nil {
							uploadErr = fmt.Errorf("upload failed for part %d with error %v", urlNumber+1, hresp.StatusCode())
							return
						}
					}
				}(i)

			}
			wg.Wait()
			if uploadErr != nil {
				return nil, uploadErr
			}
		} else {
			reqEnv.ObjectUrl = createEnv.ObjectUrl
			// Read one more byte to check if max is crossed
			b, err := io.ReadAll(io.LimitReader(reader, int64(RequestObjectMax)+1))
			if err != nil {
				return nil, err
			}
			if len(b) > RequestObjectMax {
				return nil, fmt.Errorf("payload too large for this device")
			}
			reader = bytes.NewReader(b)
			hresp, err := resty.New().R().
				SetBody(reader).
				SetDoNotParseResponse(true).
				Put(createEnv.ObjectUrl)
			if err != nil {
				return nil, err
			}
			if hresp.StatusCode() != http.StatusOK {
				return nil, fmt.Errorf("failed to upload request object: %s", hresp.Status())
			}
		}

	} else {
		// Request size does not require ObjectStore
		reqEnv.Body = base64.StdEncoding.EncodeToString(payload[0:payloadLength])
	}

	// Trigger query
	respEnv := queryResponse{}
	queryPath := fmt.Sprintf(directModePath, url.PathEscape(d.ID()), "/query")

	regionalClient := d.tenant.regionalHttpClients[d.Fqdn()]
	if regionalClient == nil {
		return nil, fmt.Errorf("%s - %s", clientRegionError, d.Fqdn())
	}

	resp, err := regionalClient.R().
		SetHeader(X_API_PROXY_COMMUNICATION_SYTLE, "sync").
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
	pollDuration := StatusPollTimeMin
	for respEnv.Status == "RUNNING" {
		// Sleep
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-time.After(pollDuration):
		}

		// Poll
		queryPath := fmt.Sprintf(directModePath, url.PathEscape(d.ID()), "/query/"+queryId)

		regionalClient := d.tenant.regionalHttpClients[d.Fqdn()]
		if regionalClient == nil {
			return nil, fmt.Errorf("%s - %s", clientRegionError, d.Fqdn())
		}

		resp, err = regionalClient.R().
			SetHeader(X_API_PROXY_COMMUNICATION_SYTLE, "sync").
			SetBody(respEnv).
			SetResult(&respEnv).
			Get(queryPath)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode() != http.StatusOK {
			return nil, fmt.Errorf("request error %s", resp.Status())
		}

		if pollDuration < StatusPollTimeMax {
			pollDuration *= 2
			if pollDuration > StatusPollTimeMax {
				pollDuration = StatusPollTimeMax
			}
		}
	}

	// Check body or object in response
	var reader io.ReadCloser
	if respEnv.ObjectUrl != "" {
		// Download object
		regionalClient := d.tenant.regionalHttpClients[d.Fqdn()]
		if regionalClient == nil {
			return nil, fmt.Errorf("%s - %s", clientRegionError, d.Fqdn())
		}

		hresp, err := regionalClient.R().
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
	regionalClient := d.tenant.regionalHttpClients[d.Fqdn()]
	if regionalClient == nil {
		return nil, fmt.Errorf("%s - %s", clientRegionError, d.Fqdn())
	}
	req := regionalClient.R()

	if request.Header != nil {
		for name, values := range request.Header {
			req.SetHeader(name, values[0])
		}
	}
	req.SetHeader(X_API_PROXY_COMMUNICATION_SYTLE, "sync")
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

	// Delete query
	queryPath := fmt.Sprintf(directModePath, url.PathEscape(q.device.ID()), "/query/"+q.queryId)
	req := q.device.tenant.regionalHttpClients[q.device.Fqdn()].R()
	req.SetHeader(X_API_PROXY_COMMUNICATION_SYTLE, "sync")
	_, err := req.Execute(http.MethodDelete, queryPath)
	return err
}
