package main_test

import (
	"encoding/json"
	"fmt"
	"github.com/cloudfoundry/gunk/runner_support"
	"github.com/onsi/ginkgo/config"
	. "github.com/vito/cmdtest/matchers"
	"net"
	"net/http"
	"os/exec"
	"testing"
	"time"

	"github.com/cloudfoundry-incubator/metricz/collector_registrar"
	"github.com/cloudfoundry/gunk/natsrunner"
	"github.com/cloudfoundry/storeadapter/storerunner/etcdstorerunner"
	"github.com/cloudfoundry/yagnats"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/vito/cmdtest"
)

func TestStager(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Stager Suite")
}

var _ = Describe("Main", func() {
	var (
		nats          *natsrunner.NATSRunner
		etcdRunner    *etcdstorerunner.ETCDClusterRunner
		stagerSession *cmdtest.Session
	)

	BeforeEach(func() {
		var err error

		natsPort := 4228 + 0 // config.GinkgoConfig.ParallelNode
		etcdPort := 5001 + config.GinkgoConfig.ParallelNode

		nats = natsrunner.NewNATSRunner(natsPort)
		nats.Start()

		etcdRunner = etcdstorerunner.NewETCDClusterRunner(etcdPort, 1)
		etcdRunner.Start()

		stagerPath, err := cmdtest.Build("github.com/cloudfoundry-incubator/stager")
		stagerSession, err = cmdtest.StartWrapped(exec.Command(
			stagerPath,
			"-etcdCluster", fmt.Sprintf("http://127.0.0.1:%d", etcdPort),
			"-natsAddresses", fmt.Sprintf("127.0.0.1:%d", natsPort),

			"-metricsPort", "5678",
			"-metricsUsername", "the-username",
			"-metricsPassword", "the-password",
			"-index", "5",
		), runner_support.TeeToGinkgoWriter, runner_support.TeeToGinkgoWriter)

		Ω(stagerSession).Should(SayWithTimeout("stager.watching-for-completed-runonce", 5*time.Second))

		Ω(err).ShouldNot(HaveOccurred())
	})

	AfterEach(func() {
		nats.Stop()
		etcdRunner.Stop()
		stagerSession.Cmd.Process.Kill()
	})

	It("starts the stager correctly", func(done Done) {
		var (
			err error
			reg collector_registrar.AnnounceComponentMessage
		)

		receivedAnnounce := make(chan bool)
		nats.MessageBus.Subscribe("discover-reply", func(message *yagnats.Message) {
			err := json.Unmarshal(message.Payload, &reg)
			Ω(err).ShouldNot(HaveOccurred())
			receivedAnnounce <- true
		})

		err = nats.MessageBus.PublishWithReplyTo("vcap.component.discover", "discover-reply", []byte{})
		Ω(err).ShouldNot(HaveOccurred())
		<-receivedAnnounce

		Eventually(func() error {
			conn, err := net.Dial("tcp", reg.Host)
			defer conn.Close()
			return err
		}).ShouldNot(HaveOccurred())

		req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/varz", reg.Host), nil)
		req.SetBasicAuth("the-username", "the-password")
		Ω(err).ShouldNot(HaveOccurred())

		Ω(reg.Index).Should(Equal(uint(5)))

		resp, err := http.DefaultClient.Do(req)
		Ω(err).ShouldNot(HaveOccurred())
		Ω(resp.StatusCode).Should(Equal(200))

		close(done)
	}, 60)
})
