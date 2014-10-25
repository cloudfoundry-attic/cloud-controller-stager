package main_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cloudfoundry/gunk/diegonats"
	"github.com/cloudfoundry/gunk/timeprovider"
	"github.com/pivotal-golang/lager/lagertest"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/ginkgomon"

	"github.com/cloudfoundry-incubator/receptor"
	Bbs "github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/stager/cmd/stager/testrunner"
	"github.com/cloudfoundry/storeadapter/storerunner/etcdstorerunner"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("Stager", func() {
	var (
		gnatsdRunner      ifrit.Process
		natsClient        diegonats.NATSClient
		fakeServer        *ghttp.Server
		bbs               *Bbs.BBS
		fileServerProcess ifrit.Process
	)

	BeforeEach(func() {
		etcdPort := 5001 + GinkgoParallelNode()
		natsPort := 4001 + GinkgoParallelNode()

		fakeServer = ghttp.NewServer()

		etcdRunner = etcdstorerunner.NewETCDClusterRunner(etcdPort, 1)
		etcdRunner.Start()

		gnatsdRunner, natsClient = diegonats.StartGnatsd(natsPort)

		bbs = Bbs.NewBBS(etcdRunner.Adapter(), timeprovider.NewTimeProvider(), lagertest.NewTestLogger("test"))

		fileServerProcess = ifrit.Envoke(bbs.NewFileServerHeartbeat("http://example.com", "file-server-id", time.Second))

		runner = testrunner.New(
			stagerPath,
			[]string{fmt.Sprintf("http://127.0.0.1:%d", etcdPort)},
			[]string{fmt.Sprintf("127.0.0.1:%d", natsPort)},

			// TODO - remove this slice (currently, we need to remove the 'http://' prefix)
			fakeServer.URL()[7:],
		)
	})

	AfterEach(func() {
		runner.Stop()
		etcdRunner.Stop()
		ginkgomon.Kill(fileServerProcess)
		ginkgomon.Kill(gnatsdRunner)
	})

	Context("when started", func() {
		BeforeEach(func() {
			runner.Start("--circuses", `{"lucid64":"lifecycle.zip"}`, "--minDiskMB", "2048", "--minMemoryMB", "256", "--minFileDescriptors", "2")
		})

		Describe("when a 'diego.staging.start' message is recieved", func() {
			BeforeEach(func() {
				fakeServer.RouteToHandler("POST", "/tasks", func(w http.ResponseWriter, req *http.Request) {
					var taskRequest receptor.CreateTaskRequest
					err := json.NewDecoder(req.Body).Decode(&taskRequest)
					Ω(err).ShouldNot(HaveOccurred())

					Ω(taskRequest.MemoryMB).Should(Equal(1024))
					Ω(taskRequest.DiskMB).Should(Equal(2048))
				})

				natsClient.Publish("diego.staging.start", []byte(`
				      {
				        "app_id":"my-app-guid",
                "task_id":"my-task-guid",
                "stack":"lucid64",
                "app_bits_download_uri":"http://example.com/app_bits",
                "file_descriptors":3,
                "memory_mb" : 1024,
                "disk_mb" : 128,
                "buildpacks" : [],
                "environment" : []
				      }
				    `))
			})

			It("desires a staging task via the API", func() {
				Eventually(fakeServer.ReceivedRequests).Should(HaveLen(1))
			})

			It("does not exit", func() {
				Consistently(runner.Session()).ShouldNot(gexec.Exit())
			})
		})

		Describe("when a 'diego.docker.staging.start' message is recieved", func() {
			BeforeEach(func() {
				fakeServer.RouteToHandler("POST", "/tasks", func(w http.ResponseWriter, req *http.Request) {
					var taskRequest receptor.CreateTaskRequest
					err := json.NewDecoder(req.Body).Decode(&taskRequest)
					Ω(err).ShouldNot(HaveOccurred())

					Ω(taskRequest.MemoryMB).Should(Equal(1024))
					Ω(taskRequest.DiskMB).Should(Equal(2048))
				})

				natsClient.Publish("diego.docker.staging.start", []byte(`
				      {
				        "app_id":"my-app-guid",
                "task_id":"my-task-guid",
                "stack":"lucid64",
                "docker_image_url":"http://docker.docker/docker",
                "file_descriptors":3,
                "memory_mb" : 1024,
                "disk_mb" : 128,
                "environment" : []
				      }
				    `))
			})

			It("desires a staging task via the API", func() {
				Eventually(fakeServer.ReceivedRequests).Should(HaveLen(1))
			})

			It("does not exit", func() {
				Consistently(runner.Session()).ShouldNot(gexec.Exit())
			})
		})
	})
})
