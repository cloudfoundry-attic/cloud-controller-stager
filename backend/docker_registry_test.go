package backend_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	. "github.com/cloudfoundry-incubator/stager/backend"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	"github.com/pivotal-golang/lager"
)

var _ = Describe("DockerBackend", func() {
	var (
		stagingRequest       cc_messages.DockerStagingRequestFromCC
		stagingRequestJson   []byte
		downloadTailorAction models.Action
		runAction            models.Action
		server               *ghttp.Server
		backend              Backend
		dockerRegistryIPs    []string
		dockerRegistryPort   uint16
		expectedEgressRules  []models.SecurityGroupRule
		dockerRegistryURL    string
	)

	dockerRegistryIPs = []string{"10.244.2.6", "10.244.2.7"}
	dockerRegistryPort = uint16(8080)

	setupConsulAgent := func() {
		server.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/v1/catalog/service/docker_registry"),
				http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					w.Write([]byte(fmt.Sprintf(
						`[
								{"Address": "%s"},
								{"Address": "%s"}
						 ]`,
						dockerRegistryIPs[0], dockerRegistryIPs[1])))
				}),
			),
		)
	}

	setupEgressRules := func(ips []string) []models.SecurityGroupRule {
		rules := []models.SecurityGroupRule{}
		for _, ip := range ips {
			rules = append(rules, models.SecurityGroupRule{
				Protocol:     models.TCPProtocol,
				Destinations: []string{ip},
				Ports:        []uint16{dockerRegistryPort},
			})
		}
		return rules
	}

	setupDockerRegistries := func(ips []string, port uint16) string {
		var result []string
		for _, ip := range ips {
			result = append(result, fmt.Sprintf("%s:%d", ip, port))
		}
		return strings.Join(result, ",")
	}

	JustBeforeEach(func() {
		server = ghttp.NewServer()
		setupConsulAgent()

		config := Config{
			FileServerURL:  "http://file-server.com",
			ConsulAgentURL: server.URL(),
		}

		if len(dockerRegistryURL) > 0 {
			config.DockerRegistry = &DockerRegistry{
				URL:      dockerRegistryURL,
				Insecure: strings.HasPrefix(dockerRegistryURL, "http://"),
			}
		}

		logger := lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))

		backend = NewDockerBackend(config, logger)

		downloadTailorAction = models.EmitProgressFor(
			&models.DownloadAction{
				From:     "http://file-server.com/v1/static/docker_app_lifecycle.zip",
				To:       "/tmp/docker_app_lifecycle",
				CacheKey: "builder-docker",
			},
			"",
			"",
			"Failed to set up docker environment",
		)

		expectedEgressRules = setupEgressRules(dockerRegistryIPs)

		stagingRequest = cc_messages.DockerStagingRequestFromCC{
			AppId:           "bunny",
			TaskId:          "hop",
			DockerImageUrl:  "busybox",
			Stack:           "rabbit_hole",
			FileDescriptors: 512,
			MemoryMB:        512,
			DiskMB:          512,
			Timeout:         512,
		}

		var err error
		stagingRequestJson, err = json.Marshal(stagingRequest)
		Ω(err).ShouldNot(HaveOccurred())
	})

	checkStagingInstructionsFunc := func() {
		desiredTask, err := backend.BuildRecipe(stagingRequestJson)
		Ω(err).ShouldNot(HaveOccurred())

		actions := actionsFromDesiredTask(desiredTask)
		Ω(actions).Should(HaveLen(2))
		Ω(actions[0]).Should(Equal(downloadTailorAction))
		Ω(actions[1]).Should(Equal(runAction))

		Ω(desiredTask.EgressRules).Should(ConsistOf(expectedEgressRules))
	}

	Context("when Docker Registry is insecure", func() {
		BeforeEach(func() {
			dockerRegistryURL = fmt.Sprintf("http://%s:%d", dockerRegistryIPs[0], dockerRegistryPort)
		})

		JustBeforeEach(func() {
			fileDescriptorLimit := uint64(512)
			dockerRegistries := setupDockerRegistries(dockerRegistryIPs, dockerRegistryPort)
			runAction = models.EmitProgressFor(
				&models.RunAction{
					Path: "/tmp/docker_app_lifecycle/builder",
					Args: []string{
						"-outputMetadataJSONFilename",
						"/tmp/docker-result/result.json",
						"-dockerRef",
						"busybox",
						"-dockerRegistryAddresses",
						dockerRegistries,
						"-insecureDockerRegistries",
						dockerRegistries,
						"-cacheDockerImage",
					},
					Env: []models.EnvironmentVariable{},
					ResourceLimits: models.ResourceLimits{
						Nofile: &fileDescriptorLimit,
					},
					Privileged: true,
				},
				"Staging...",
				"Staging Complete",
				"Staging Failed",
			)
		})

		It("creates a cf-app-docker-staging Task with staging instructions", checkStagingInstructionsFunc)
	})

	Context("when Docker Registry is secure", func() {
		BeforeEach(func() {
			dockerRegistryURL = fmt.Sprintf("https://%s:%d", dockerRegistryIPs[0], dockerRegistryPort)
		})

		JustBeforeEach(func() {
			fileDescriptorLimit := uint64(512)
			dockerRegistries := setupDockerRegistries(dockerRegistryIPs, dockerRegistryPort)
			runAction = models.EmitProgressFor(
				&models.RunAction{
					Path: "/tmp/docker_app_lifecycle/builder",
					Args: []string{
						"-outputMetadataJSONFilename",
						"/tmp/docker-result/result.json",
						"-dockerRef",
						"busybox",
						"-dockerRegistryAddresses",
						dockerRegistries,
						"-cacheDockerImage",
					},
					Env: []models.EnvironmentVariable{},
					ResourceLimits: models.ResourceLimits{
						Nofile: &fileDescriptorLimit,
					},
					Privileged: true,
				},
				"Staging...",
				"Staging Complete",
				"Staging Failed",
			)
		})

		It("creates a cf-app-docker-staging Task with staging instructions", checkStagingInstructionsFunc)
	})

	Context("with no docker registry URL", func() {
		BeforeEach(func() {
			dockerRegistryURL = ""
		})

		It("creates a cf-app-docker-staging Task with no additional egress rules", func() {
			desiredTask, err := backend.BuildRecipe(stagingRequestJson)
			Ω(err).ShouldNot(HaveOccurred())
			Ω(desiredTask.EgressRules).Should(BeEmpty())
		})
	})
})
