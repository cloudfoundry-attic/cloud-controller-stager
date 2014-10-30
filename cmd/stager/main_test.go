package main_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/cloudfoundry/gunk/diegonats"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/ginkgomon"

	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/stager/cmd/stager/testrunner"
	"github.com/cloudfoundry-incubator/stager/stager"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("Stager", func() {
	var (
		stagerPort   int
		gnatsdRunner ifrit.Process
		natsClient   diegonats.NATSClient
		fakeServer   *ghttp.Server
		fakeCC       *ghttp.Server
	)

	BeforeEach(func() {
		natsPort := 4001 + GinkgoParallelNode()
		stagerPort = 8888 + GinkgoParallelNode()

		fakeServer = ghttp.NewServer()
		fakeServerURL, err := url.Parse(fakeServer.URL())
		Ω(err).ShouldNot(HaveOccurred())

		gnatsdRunner, natsClient = diegonats.StartGnatsd(natsPort)

		fakeCC = ghttp.NewServer()

		runner = testrunner.New(testrunner.Config{
			StagerBin:     stagerPath,
			StagerURL:     fmt.Sprintf("http://127.0.0.1:%d", stagerPort),
			NatsAddresses: []string{fmt.Sprintf("127.0.0.1:%d", natsPort)},
			DiegoAPIURL:   fakeServerURL.Host,
			CCBaseURL:     fakeCC.URL(),
		})
	})

	AfterEach(func() {
		runner.Stop()
		ginkgomon.Kill(gnatsdRunner)
	})

	Context("when started", func() {
		BeforeEach(func() {
			runner.Start("--circuses", `{"lucid64":"lifecycle.zip"}`, "--minDiskMB", "2048", "--minMemoryMB", "256", "--minFileDescriptors", "2")
		})

		Describe("when a 'diego.staging.start' message is received", func() {
			It("desires a staging task via the API", func() {
				fakeServer.RouteToHandler("POST", "/tasks", func(w http.ResponseWriter, req *http.Request) {
					var taskRequest receptor.CreateTaskRequest
					err := json.NewDecoder(req.Body).Decode(&taskRequest)
					Ω(err).ShouldNot(HaveOccurred())

					Ω(taskRequest.MemoryMB).Should(Equal(1024))
					Ω(taskRequest.DiskMB).Should(Equal(2048))
					Ω(taskRequest.CompletionCallbackURL).Should(Equal(runner.Config.StagerURL))
				})

				natsClient.Publish("diego.staging.start", []byte(`{
					"app_id":"my-app-guid",
					"task_id":"my-task-guid",
					"stack":"lucid64",
					"app_bits_download_uri":"http://example.com/app_bits",
					"file_descriptors":3,
					"memory_mb" : 1024,
					"disk_mb" : 128,
					"buildpacks" : [],
					"environment" : []
				}`))

				Eventually(fakeServer.ReceivedRequests).Should(HaveLen(1))
				Consistently(runner.Session()).ShouldNot(gexec.Exit())
			})
		})

		Describe("when a 'diego.docker.staging.start' message is recieved", func() {
			It("desires a staging task via the API", func() {
				fakeServer.RouteToHandler("POST", "/tasks", func(w http.ResponseWriter, req *http.Request) {
					var taskRequest receptor.CreateTaskRequest
					err := json.NewDecoder(req.Body).Decode(&taskRequest)
					Ω(err).ShouldNot(HaveOccurred())

					Ω(taskRequest.MemoryMB).Should(Equal(1024))
					Ω(taskRequest.DiskMB).Should(Equal(2048))
					Ω(taskRequest.CompletionCallbackURL).Should(Equal(runner.Config.StagerURL))
				})

				natsClient.Publish("diego.docker.staging.start", []byte(`{
					"app_id":"my-app-guid",
					"task_id":"my-task-guid",
					"stack":"lucid64",
					"docker_image_url":"http://docker.docker/docker",
					"file_descriptors":3,
					"memory_mb" : 1024,
					"disk_mb" : 128,
					"environment" : []
				}`))

				Eventually(fakeServer.ReceivedRequests).Should(HaveLen(1))
				Consistently(runner.Session()).ShouldNot(gexec.Exit())
			})
		})

		Describe("when a staging task completes", func() {
			var resp *http.Response

			BeforeEach(func() {
				fakeCC.RouteToHandler("POST", "/internal/staging/completed", func(res http.ResponseWriter, req *http.Request) {
				})

				taskJSON, err := json.Marshal(receptor.TaskResponse{
					TaskGuid:   "the-task-guid",
					Domain:     stager.TaskDomain,
					Annotation: `{}`,
					Result:     `{}`,
				})
				Ω(err).ShouldNot(HaveOccurred())

				resp, err = http.Post(runner.Config.StagerURL, "application/json", bytes.NewReader(taskJSON))
				Ω(err).ShouldNot(HaveOccurred())
			})

			It("POSTs to the CC that staging is complete", func() {
				Eventually(fakeCC.ReceivedRequests).Should(HaveLen(1))
			})
		})
	})
})
