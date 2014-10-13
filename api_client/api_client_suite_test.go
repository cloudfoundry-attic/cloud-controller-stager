package api_client_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"testing"
)

func TestApiClient(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ApiClient Suite")
}
