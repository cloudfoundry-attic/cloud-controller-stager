package api_client_test

import (
	"io/ioutil"
	"net/http"
	"net/url"

	"github.com/cloudfoundry-incubator/stager/api_client"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	"github.com/pivotal-golang/lager"
)

var _ = Describe("API Client", func() {
	var (
		fakeCC *ghttp.Server

		logger    lager.Logger
		apiClient api_client.ApiClient
	)

	BeforeEach(func() {
		fakeCC = ghttp.NewServer()

		logger = lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))

		apiClient = api_client.NewApiClient(fakeCC.URL(), "username", "password", true)
	})

	AfterEach(func() {
		if fakeCC.HTTPTestServer != nil {
			fakeCC.Close()
		}
	})

	Describe("Successfully calling the Cloud Controller", func() {
		var expectedBody = []byte(`{ "key": "value" }`)

		BeforeEach(func() {
			fakeCC.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/internal/staging/completed"),
					ghttp.VerifyBasicAuth("username", "password"),
					ghttp.RespondWith(200, `{}`),
					func(w http.ResponseWriter, req *http.Request) {
						body, err := ioutil.ReadAll(req.Body)
						defer req.Body.Close()

						Ω(err).ShouldNot(HaveOccurred())
						Ω(body).Should(Equal(expectedBody))
					},
				),
			)
		})

		It("sends the request payload to the CC without modification", func() {
			err := apiClient.StagingComplete(expectedBody, logger)
			Ω(err).ShouldNot(HaveOccurred())
		})
	})

	Describe("TLS certificate validation", func() {
		BeforeEach(func() {
			fakeCC = ghttp.NewTLSServer() // self-signed certificate
			fakeCC.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/internal/staging/completed"),
					ghttp.VerifyBasicAuth("username", "password"),
					ghttp.RespondWith(200, `{}`),
				),
			)
		})

		Context("when certificate verfication is enabled", func() {
			BeforeEach(func() {
				apiClient = api_client.NewApiClient(fakeCC.URL(), "username", "password", false)
			})

			It("fails with a self-signed certificate", func() {
				err := apiClient.StagingComplete([]byte(`{}`), logger)
				Ω(err).Should(HaveOccurred())
			})
		})

		Context("when certificate verfication is disabled", func() {
			BeforeEach(func() {
				apiClient = api_client.NewApiClient(fakeCC.URL(), "username", "password", true)
			})

			It("Attempts to validate SSL certificates", func() {
				err := apiClient.StagingComplete([]byte(`{}`), logger)
				Ω(err).ShouldNot(HaveOccurred())
			})
		})
	})

	Describe("Error conditions", func() {
		Context("when the request couldn't be completed", func() {
			BeforeEach(func() {
				bogusURL := "http://0.0.0.0.0:80"
				apiClient = api_client.NewApiClient(bogusURL, "username", "password", true)
			})

			It("percolates the error", func() {
				err := apiClient.StagingComplete([]byte(`{}`), logger)
				Ω(err).Should(HaveOccurred())
				Ω(err).Should(BeAssignableToTypeOf(&url.Error{}))
			})
		})

		Context("when the response code is not StatusOK (200)", func() {
			BeforeEach(func() {
				fakeCC.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("POST", "/internal/staging/completed"),
						ghttp.RespondWith(500, `{}`),
					),
				)
			})

			It("returns an error with the actual status code", func() {
				err := apiClient.StagingComplete([]byte(`{}`), logger)
				Ω(err).Should(HaveOccurred())
				Ω(err).Should(BeAssignableToTypeOf(&api_client.BadResponseError{}))
				Ω(err.(*api_client.BadResponseError).StatusCode).Should(Equal(500))
			})
		})
	})
})
