package integration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cloudfoundry/gunk/natsrunner"
	"github.com/cloudfoundry/gunk/timeprovider"
	"github.com/cloudfoundry/yagnats"
	"github.com/pivotal-golang/lager/lagertest"
	"github.com/tedsuo/ifrit"

	Bbs "github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/stager/integration/stager_runner"
	"github.com/cloudfoundry/storeadapter/storerunner/etcdstorerunner"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

var stagerPath string
var etcdRunner *etcdstorerunner.ETCDClusterRunner
var natsRunner *natsrunner.NATSRunner
var runner *stager_runner.StagerRunner

var _ = Describe("Main", func() {
	var (
		natsClient        yagnats.NATSClient
		bbs               *Bbs.BBS
		fileServerProcess ifrit.Process
	)

	BeforeEach(func() {
		etcdPort := 5001 + GinkgoParallelNode()
		natsPort := 4001 + GinkgoParallelNode()

		etcdRunner = etcdstorerunner.NewETCDClusterRunner(etcdPort, 1)
		etcdRunner.Start()

		natsRunner = natsrunner.NewNATSRunner(natsPort)
		natsRunner.Start()

		natsClient = natsRunner.MessageBus

		bbs = Bbs.NewBBS(etcdRunner.Adapter(), timeprovider.NewTimeProvider(), lagertest.NewTestLogger("test"))

		fileServerProcess = ifrit.Envoke(bbs.NewFileServerHeartbeat("http://example.com", "file-server-id", time.Second))

		runner = stager_runner.New(
			stagerPath,
			[]string{fmt.Sprintf("http://127.0.0.1:%d", etcdPort)},
			[]string{fmt.Sprintf("127.0.0.1:%d", natsPort)},
		)
	})

	AfterEach(func(done Done) {
		runner.Stop()
		fileServerProcess.Signal(os.Kill)
		etcdRunner.Stop()
		natsRunner.Stop()
		close(done)
	}, 10.0)

	Context("when started", func() {
		BeforeEach(func() {
			runner.Start("--circuses", `{"lucid64":"lifecycle.zip"}`, "--minDiskMB", "2048", "--minMemoryMB", "256", "--minFileDescriptors", "2")
		})

		Describe("when a 'diego.staging.start' message is recieved", func() {
			BeforeEach(func() {
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

			It("desires a staging task via the BBS", func() {
				Eventually(bbs.GetAllPendingTasks, 1.0).Should(HaveLen(1))
				tasks, err := bbs.GetAllPendingTasks()
				Ω(err).ShouldNot(HaveOccurred())
				Ω(tasks[0].MemoryMB).Should(Equal(1024))
				Ω(tasks[0].DiskMB).Should(Equal(2048))
			})

			It("does not exit", func() {
				Consistently(runner.Session()).ShouldNot(gexec.Exit())
			})
		})

		Describe("when a 'diego.docker.staging.start' message is recieved", func() {
			var stagingFinished chan cc_messages.DockerStagingResponseForCC

			BeforeEach(func() {
				// local var to prevent data race with callback
				finished := make(chan cc_messages.DockerStagingResponseForCC, 1)

				stagingFinished = finished

				natsClient.Subscribe("diego.docker.staging.finished", func(msg *yagnats.Message) {
					stagingMsg := cc_messages.DockerStagingResponseForCC{}
					err := json.Unmarshal(msg.Payload, &stagingMsg)
					Ω(err).ShouldNot(HaveOccurred())
					finished <- stagingMsg
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

			It("sends a docker staging finished NATS message", func() {
				expectedMsg := cc_messages.DockerStagingResponseForCC{
					AppId:  "my-app-guid",
					TaskId: "my-task-guid",
				}
				Eventually(stagingFinished).Should(Receive(&expectedMsg))
			})

			It("does not exit", func() {
				Consistently(runner.Session()).ShouldNot(gexec.Exit())
			})
		})
	})
})

func TestStagerMain(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration Suite")
}

var _ = BeforeSuite(func() {
	var err error
	stagerPath, err = gexec.Build("github.com/cloudfoundry-incubator/stager", "-race")
	Ω(err).ShouldNot(HaveOccurred())
})

var _ = AfterSuite(func() {
	gexec.CleanupBuildArtifacts()
	if etcdRunner != nil {
		etcdRunner.Stop()
	}
	if natsRunner != nil {
		natsRunner.Stop()
	}
	if runner != nil {
		runner.Stop()
	}
})
