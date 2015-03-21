package cc_client_test

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"

	"github.com/cloudfoundry-incubator/stager/cc_client"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	"github.com/pivotal-golang/lager"
)

var _ = Describe("CC Client", func() {
	var (
		fakeCC *ghttp.Server

		logger   lager.Logger
		ccClient cc_client.CcClient

		stagingGuid string
	)

	BeforeEach(func() {
		fakeCC = ghttp.NewServer()

		logger = lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))

		ccClient = cc_client.NewCcClient(fakeCC.URL(), "username", "password", true)

		stagingGuid = "the-staging-guid"
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
					ghttp.VerifyRequest("POST", fmt.Sprintf("/internal/staging/%s/completed", stagingGuid)),
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
			err := ccClient.StagingComplete(stagingGuid, expectedBody, logger)
			Ω(err).ShouldNot(HaveOccurred())
		})
	})

	Describe("TLS certificate validation", func() {
		BeforeEach(func() {
			fakeCC = ghttp.NewTLSServer() // self-signed certificate
			fakeCC.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", fmt.Sprintf("/internal/staging/%s/completed", stagingGuid)),
					ghttp.VerifyBasicAuth("username", "password"),
					ghttp.RespondWith(200, `{}`),
				),
			)

			// muffle server-side log of certificate error
			fakeCC.HTTPTestServer.Config.ErrorLog = log.New(ioutil.Discard, "", log.Flags())
		})

		Context("when certificate verfication is enabled", func() {
			BeforeEach(func() {
				ccClient = cc_client.NewCcClient(fakeCC.URL(), "username", "password", false)
			})

			It("fails with a self-signed certificate", func() {
				err := ccClient.StagingComplete(stagingGuid, []byte(`{}`), logger)
				Ω(err).Should(HaveOccurred())
			})
		})

		Context("when certificate verfication is disabled", func() {
			BeforeEach(func() {
				ccClient = cc_client.NewCcClient(fakeCC.URL(), "username", "password", true)
			})

			It("Attempts to validate SSL certificates", func() {
				err := ccClient.StagingComplete(stagingGuid, []byte(`{}`), logger)
				Ω(err).ShouldNot(HaveOccurred())
			})
		})
	})

	Describe("Error conditions", func() {
		Context("when the request couldn't be completed", func() {
			BeforeEach(func() {
				bogusURL := "http://0.0.0.0.0:80"
				ccClient = cc_client.NewCcClient(bogusURL, "username", "password", true)
			})

			It("percolates the error", func() {
				err := ccClient.StagingComplete(stagingGuid, []byte(`{}`), logger)
				Ω(err).Should(HaveOccurred())
				Ω(err).Should(BeAssignableToTypeOf(&url.Error{}))
			})
		})

		Context("when the response code is not StatusOK (200)", func() {
			BeforeEach(func() {
				fakeCC.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("POST", fmt.Sprintf("/internal/staging/%s/completed", stagingGuid)),
						ghttp.RespondWith(500, `{}`),
					),
				)
			})

			It("returns an error with the actual status code", func() {
				err := ccClient.StagingComplete(stagingGuid, []byte(`{}`), logger)
				Ω(err).Should(HaveOccurred())
				Ω(err).Should(BeAssignableToTypeOf(&cc_client.BadResponseError{}))
				Ω(err.(*cc_client.BadResponseError).StatusCode).Should(Equal(500))
			})
		})
	})

	Describe("IsRetryable", func() {
		Context("when the error is a net.Error", func() {
			It("is not retryable", func() {
				err := &testNetError{}
				Ω(cc_client.IsRetryable(err)).To(BeFalse())
			})

			Context("when the error is temporary", func() {
				It("is retryable", func() {
					err := &testNetError{timeout: false, temporary: true}
					Ω(cc_client.IsRetryable(err)).To(BeTrue())
				})
			})

			Context("when the error is a timeout", func() {
				It("is retryable", func() {
					err := &testNetError{timeout: true, temporary: false}
					Ω(cc_client.IsRetryable(err)).To(BeTrue())
				})
			})
		})

		Context("when the error is a BadResponseError", func() {
			It("is not retryable", func() {
				err := &cc_client.BadResponseError{}
				Ω(cc_client.IsRetryable(err)).To(BeFalse())
			})

			Context("when the response code is StatusServiceUnavailable", func() {
				It("is retryable", func() {
					err := &cc_client.BadResponseError{http.StatusServiceUnavailable}
					Ω(cc_client.IsRetryable(err)).To(BeTrue())
				})
			})

			Context("when the response code is StatusGatewayTimeout", func() {
				It("is retryable", func() {
					err := &cc_client.BadResponseError{http.StatusGatewayTimeout}
					Ω(cc_client.IsRetryable(err)).To(BeTrue())
				})
			})
		})

		Context("general errors", func() {
			It("is not retryable", func() {
				err := fmt.Errorf("A generic error")
				Ω(cc_client.IsRetryable(err)).To(BeFalse())
			})
		})
	})
})

type testNetError struct {
	timeout   bool
	temporary bool
}

func (e *testNetError) Error() string   { return "test error" }
func (e *testNetError) Timeout() bool   { return e.timeout }
func (e *testNetError) Temporary() bool { return e.temporary }
