package metrics_test

import (
	"encoding/json"
	"github.com/cloudfoundry-incubator/metricz/localip"
	"io/ioutil"
	"net/http"
	"strings"

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
	)

	BeforeEach(func() {
		fakenats = fakeyagnats.New()
		logger = steno.NewLogger("fakelogger")
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
		})

		It("announces to the collector with the right type, port, credentials and index", func(done Done) {
			Listen(fakenats, logger, Config{
				Port:     34567,
				Username: "the-username",
				Password: "the-password",
				Index:    3,
			})

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

		It("exposes a varz endpoint on the given port", func() {
			Listen(fakenats, logger, Config{
				Port:     34567,
				Username: "the-username",
				Password: "the-password",
				Index:    3,
			})

			request, _ := http.NewRequest("GET", "http://"+myIP+":34567/varz", nil)
			request.SetBasicAuth("the-username", "the-password")
			response, err := http.DefaultClient.Do(request)

			Ω(err).ShouldNot(HaveOccurred())

			bytes, _ := ioutil.ReadAll(response.Body)
			data := make(map[string]interface{})
			json.Unmarshal(bytes, &data)

			Ω(data["name"]).Should(Equal("Stager"))
		})

		It("exposes a healthz endpoint on the given port", func() {
			Listen(fakenats, logger, Config{
				Port:     34567,
				Username: "the-username",
				Password: "the-password",
				Index:    3,
			})

			request, _ := http.NewRequest("GET", "http://"+myIP+":34567/healthz", nil)
			request.SetBasicAuth("the-username", "the-password")
			response, err := http.DefaultClient.Do(request)

			Ω(err).ShouldNot(HaveOccurred())

			Ω(response.StatusCode).To(Equal(200))
		})
	})
})
