package api_client

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/cloudfoundry/gunk/urljoiner"
	"github.com/pivotal-golang/lager"
)

const (
	stagingCompletePath           = "/internal/staging/completed"
	stagingCompleteRequestTimeout = 900 * time.Second
)

type ApiClient interface {
	StagingComplete(payload []byte, logger lager.Logger) error
}

type apiClient struct {
	stagingCompleteURI string
	username           string
	password           string
	httpClient         *http.Client
}

func NewApiClient(baseURI string, username string, password string, skipCertVerify bool) ApiClient {
	httpClient := &http.Client{
		Timeout: stagingCompleteRequestTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: skipCertVerify,
			},
		},
	}

	return &apiClient{
		stagingCompleteURI: urljoiner.Join(baseURI, stagingCompletePath),
		username:           username,
		password:           password,
		httpClient:         httpClient,
	}
}

func (api *apiClient) StagingComplete(payload []byte, logger lager.Logger) error {
	logger = logger.Session("api-client")
	logger.Info("delivering-staging-response", lager.Data{"payload": string(payload)})

	request, err := http.NewRequest("POST", api.stagingCompleteURI, bytes.NewReader(payload))
	if err != nil {
		return err
	}

	request.SetBasicAuth(api.username, api.password)
	request.Header.Set("content-type", "application/json")

	response, err := api.httpClient.Do(request)
	if err != nil {
		logger.Error("deliver-staging-response-failed", err)
		return err
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		err = fmt.Errorf("Staging response POST failed with %d", response.StatusCode)
		return err
	}

	logger.Info("delivered-staging-response")
	return nil
}
