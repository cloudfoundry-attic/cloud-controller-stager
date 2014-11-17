package cc_client

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/cloudfoundry/gunk/urljoiner"
	"github.com/pivotal-golang/lager"
)

const (
	stagingCompletePath           = "/internal/staging/completed"
	stagingCompleteRequestTimeout = 5 * time.Second
)

type CcClient interface {
	StagingComplete(payload []byte, logger lager.Logger) error
}

type ccClient struct {
	stagingCompleteURI string
	username           string
	password           string
	httpClient         *http.Client
}

type BadResponseError struct {
	StatusCode int
}

func (b *BadResponseError) Error() string {
	return fmt.Sprintf("Staging response POST failed with %d", b.StatusCode)
}

func NewCcClient(baseURI string, username string, password string, skipCertVerify bool) CcClient {
	httpClient := &http.Client{
		Timeout: stagingCompleteRequestTimeout,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			Dial: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).Dial,
			TLSHandshakeTimeout: 10 * time.Second,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: skipCertVerify,
				MinVersion:         tls.VersionTLS10,
			},
		},
	}

	return &ccClient{
		stagingCompleteURI: urljoiner.Join(baseURI, stagingCompletePath),
		username:           username,
		password:           password,
		httpClient:         httpClient,
	}
}

func (cc *ccClient) StagingComplete(payload []byte, logger lager.Logger) error {
	logger = logger.Session("cc-client")
	logger.Info("delivering-staging-response", lager.Data{"payload": string(payload)})

	request, err := http.NewRequest("POST", cc.stagingCompleteURI, bytes.NewReader(payload))
	if err != nil {
		return err
	}

	request.SetBasicAuth(cc.username, cc.password)
	request.Header.Set("content-type", "application/json")

	response, err := cc.httpClient.Do(request)
	if err != nil {
		logger.Error("deliver-staging-response-failed", err)
		return err
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return &BadResponseError{response.StatusCode}
	}

	logger.Info("delivered-staging-response")
	return nil
}

func IsRetryable(err error) bool {
	if nerr, ok := err.(net.Error); ok {
		return nerr.Temporary() || nerr.Timeout()
	}

	if berr, ok := err.(*BadResponseError); ok {
		switch berr.StatusCode {
		case http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}

	return false
}