package metrics_test

import (
	"encoding/json"
	"github.com/cloudfoundry-incubator/metricz/localip"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs/fake_bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/cloudfoundry-incubator/metricz/instrumentation"
	. "github.com/cloudfoundry-incubator/stager/metrics"
	steno "github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/yagnats"
	"github.com/cloudfoundry/yagnats/fakeyagnats"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Metrics", func() {
	var (
		fakenats *fakeyagnats.FakeYagnats
		logger   *steno.Logger
		bbs      *fake_bbs.FakeMetricsBBS
		server   *MetricsServer
	)

	BeforeEach(func() {
		fakenats = fakeyagnats.New()
		bbs = fake_bbs.NewFakeMetricsBBS()
		logger = steno.NewLogger("fakelogger")
		server = NewMetricsServer(fakenats, bbs, logger, Config{
			Port:     34567,
			Username: "the-username",
			Password: "the-password",
			Index:    3,
		})
	})

	Describe("Listen", func() {
		var (
			payloadChan chan []byte
			myIP        string
		)

		BeforeEach(func() {
			var err error
			myIP, err = localip.LocalIP()
			Ω(err).ShouldNot(HaveOccurred())

			payloadChan = make(chan []byte, 1)
			fakenats.Subscribe("vcap.component.announce", func(msg *yagnats.Message) {
				payloadChan <- msg.Payload
			})

			go server.Listen()
		})

		AfterEach(func() {
			server.Stop()
		})

		It("announces to the collector with the right type, port, credentials and index", func(done Done) {
			payload := <-payloadChan
			response := make(map[string]interface{})
			json.Unmarshal(payload, &response)

			Ω(response["type"]).Should(Equal("Stager"))

			Ω(strings.HasSuffix(response["host"].(string), ":34567")).Should(BeTrue())

			Ω(response["credentials"]).Should(Equal([]interface{}{
				"the-username",
				"the-password",
			}))

			Ω(response["index"]).Should(Equal(float64(3)))

			close(done)
		}, 3)

		Describe("the varz endpoint", func() {
			BeforeEach(func() {
				bbs.GetAllRunOncesReturns.Models = []*models.RunOnce{
					&models.RunOnce{State: models.RunOnceStatePending},
					&models.RunOnce{State: models.RunOnceStatePending},
					&models.RunOnce{State: models.RunOnceStatePending},

					&models.RunOnce{State: models.RunOnceStateClaimed},
					&models.RunOnce{State: models.RunOnceStateClaimed},

					&models.RunOnce{State: models.RunOnceStateRunning},

					&models.RunOnce{State: models.RunOnceStateCompleted},
					&models.RunOnce{State: models.RunOnceStateCompleted},
					&models.RunOnce{State: models.RunOnceStateCompleted},
					&models.RunOnce{State: models.RunOnceStateCompleted},

					&models.RunOnce{State: models.RunOnceStateResolving},
					&models.RunOnce{State: models.RunOnceStateResolving},
				}

			})

			It("returns the number of tasks in each state", func() {
				request, _ := http.NewRequest("GET", "http://"+myIP+":34567/varz", nil)
				request.SetBasicAuth("the-username", "the-password")
				response, err := http.DefaultClient.Do(request)
				Ω(err).ShouldNot(HaveOccurred())
				bytes, _ := ioutil.ReadAll(response.Body)
				varzMessage := instrumentation.VarzMessage{}
				json.Unmarshal(bytes, &varzMessage)

				Ω(varzMessage.Name).Should(Equal("Stager"))
				Ω(varzMessage.Contexts[0]).Should(Equal(instrumentation.Context{
					Name: "Tasks",
					Metrics: []instrumentation.Metric{
						{
							Name:  "Pending",
							Value: float64(3),
						},
						{
							Name:  "Claimed",
							Value: float64(2),
						},
						{
							Name:  "Running",
							Value: float64(1),
						},
						{
							Name:  "Completed",
							Value: float64(4),
						},
						{
							Name:  "Resolving",
							Value: float64(2),
						},
					},
				}))
			})
		})

		Describe("the healthz endpoint", func() {
			It("returns success", func() {
				request, _ := http.NewRequest("GET", "http://"+myIP+":34567/healthz", nil)
				request.SetBasicAuth("the-username", "the-password")
				response, err := http.DefaultClient.Do(request)

				Ω(err).ShouldNot(HaveOccurred())

				Ω(response.StatusCode).To(Equal(200))
			})
		})
	})
})
