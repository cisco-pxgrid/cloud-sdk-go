package cloud

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cisco-pxgrid/cloud-sdk-go/internal/pubsub/test"
	"github.com/go-chi/render"

	"github.com/stretchr/testify/require"
)

const (
	objectStorePath = "/unittest/objectstore"
)

func setupDevice(tsUrl string) (*App, *Device, error) {
	// Construct config
	url, _ := url.Parse(tsUrl)
	config := Config{
		ID:            "appId",
		RegionalFQDN:  url.Host,
		GlobalFQDN:    url.Host,
		WriteStreamID: "writeStream",
		ReadStreamID:  "readStream",
		GetCredentials: func() (*Credentials, error) {
			return &Credentials{
				ApiKey: []byte("dummy"),
			}, nil
		},
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}
	app, err := New(config)
	if err != nil {
		return nil, nil, err
	}
	tenant, err := app.LinkTenant("otp")
	if err != nil {
		return nil, nil, err
	}
	devices, err := tenant.GetDevices()
	if err != nil {
		return nil, nil, err
	}
	// give time to connect to prevent race condition
	time.Sleep(500 * time.Millisecond)
	return app, &devices[0], nil
}

func TestEmptyQuery(t *testing.T) {
	s, _ := test.NewRPCServer(t, test.Config{
		QueryHandler: func(w http.ResponseWriter, r *http.Request) {
			result := queryResponse{
				Id:     "123",
				Status: "COMPLETE",
				Code:   http.StatusOK,
				Body:   "",
			}
			render.JSON(w, r, result)
		},
	})
	defer s.Close()

	app, device, err := setupDevice(s.URL)
	require.NoError(t, err)
	defer app.Close()

	resp, err := device.Query(&http.Request{URL: &url.URL{}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

}

func TestSmallQuery(t *testing.T) {
	s, _ := test.NewRPCServer(t, test.Config{
		QueryHandler: func(w http.ResponseWriter, r *http.Request) {
			reqEnv := envelop{}
			_ = json.NewDecoder(r.Body).Decode(&reqEnv)
			result := queryResponse{
				Id:     "123",
				Status: "COMPLETE",
				Code:   http.StatusOK,
				Body:   reqEnv.Body,
			}
			render.JSON(w, r, result)
		},
	})
	defer s.Close()

	app, device, err := setupDevice(s.URL)
	require.NoError(t, err)
	defer app.Close()

	resp, err := device.Query(&http.Request{URL: &url.URL{}, Body: io.NopCloser(strings.NewReader("body1"))})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "body1", string(b))
	resp.Body.Close()
}

func TestLargeRequest(t *testing.T) {
	s, _ := test.NewRPCServer(t, test.Config{
		QueryHandler: func(w http.ResponseWriter, r *http.Request) {
			reqEnv := envelop{}
			_ = json.NewDecoder(r.Body).Decode(&reqEnv)
			if reqEnv.QueryID == "" {
				http.Error(w, "Missing QueryId", http.StatusBadRequest)
				return
			}
			result := queryResponse{
				Id:     "123",
				Status: "COMPLETE",
				Code:   http.StatusOK,
				Body:   base64.StdEncoding.EncodeToString([]byte("small response")),
			}
			render.JSON(w, r, result)
		},
	})
	defer s.Close()

	app, device, err := setupDevice(s.URL)
	require.NoError(t, err)
	defer app.Close()

	// Lower max to test logic easier
	RequestBodyMax = 10

	resp, err := device.Query(&http.Request{URL: &url.URL{}, Body: io.NopCloser(strings.NewReader("12345678901"))})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "small response", string(b))
	resp.Body.Close()
}

func TestLargeResponse(t *testing.T) {
	var surl string
	s, _ := test.NewRPCServer(t, test.Config{
		QueryHandler: func(w http.ResponseWriter, r *http.Request) {
			result := queryResponse{
				Id:        "123",
				Status:    "COMPLETE",
				Code:      http.StatusOK,
				ObjectUrl: surl + objectStorePath,
			}
			render.JSON(w, r, result)
		},
	})
	defer s.Close()
	surl = s.URL

	app, device, err := setupDevice(s.URL)
	require.NoError(t, err)
	defer app.Close()

	resp, err := device.Query(&http.Request{URL: &url.URL{}, Body: io.NopCloser(strings.NewReader("small"))})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "object1", string(b))
	resp.Body.Close()
}

func TestStatus(t *testing.T) {
	var surl string
	var count int

	s, _ := test.NewRPCServer(t, test.Config{
		QueryHandler: func(w http.ResponseWriter, r *http.Request) {
			render.JSON(w, r, queryResponse{Id: "query1", Status: "RUNNING"})
		},
		QueryStatusHandler: func(w http.ResponseWriter, r *http.Request) {
			var result queryResponse
			if count < 2 {
				result = queryResponse{Id: "query1", Status: "RUNNING"}
			} else {
				result = queryResponse{
					Id:        "query1",
					Status:    "COMPLETE",
					Code:      http.StatusOK,
					ObjectUrl: surl + objectStorePath,
				}
			}
			count++
			render.JSON(w, r, result)
		},
	})
	defer s.Close()
	surl = s.URL

	app, device, err := setupDevice(s.URL)
	require.NoError(t, err)
	defer app.Close()

	// Lower status poll for quicker test
	StatusPollTimeMin = 100 * time.Millisecond

	resp, err := device.Query(&http.Request{URL: &url.URL{}, Body: io.NopCloser(strings.NewReader("small"))})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "object1", string(b))
	resp.Body.Close()
}

func TestFallbackQuery(t *testing.T) {
	s, r := test.NewRPCServer(t, test.Config{
		QueryHandler: func(w http.ResponseWriter, r *http.Request) {
			//not found
			http.Error(w, "Not Found", http.StatusNotFound)
		},
	})
	defer s.Close()

	r.Get("/api/dxhub/v2/apiproxy/request/{deviceId}/direct/api/get", func(w http.ResponseWriter, r *http.Request) {})

	app, device, err := setupDevice(s.URL)
	require.NoError(t, err)
	defer app.Close()

	u, _ := url.Parse("/api/get")
	resp, err := device.Query(&http.Request{Method: http.MethodGet, URL: u})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

func TestFallbackTooLargeQuery(t *testing.T) {
	s, r := test.NewRPCServer(t, test.Config{
		QueryHandler: func(w http.ResponseWriter, r *http.Request) {
			//not found
			http.Error(w, "Not Found", http.StatusNotFound)
		},
	})
	defer s.Close()

	r.Get("/api/dxhub/v2/apiproxy/request/{deviceId}/direct/api/get", func(w http.ResponseWriter, r *http.Request) {})

	app, device, err := setupDevice(s.URL)
	require.NoError(t, err)
	defer app.Close()

	// Lower max to test logic easier
	RequestBodyMax = 10

	u, _ := url.Parse("/api/get")
	_, err = device.Query(&http.Request{
		Method: http.MethodGet,
		URL:    u,
		Body:   io.NopCloser(strings.NewReader("12345678901"))})
	require.Error(t, err)
}
