package backend_test

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/stager/backend"
	"github.com/cloudfoundry-incubator/stager/helpers"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	"github.com/pivotal-golang/lager"
)

var _ = Describe("DockerBackend", func() {

	const stagingGuid = "staging-guid"
	const dockerRegistryPort = uint16(8080)
	var dockerRegistryIPs = []string{"10.244.2.6", "10.244.2.7"}

	setupDockerBackend := func(dockerRegistryURL string, payload string) backend.Backend {
		server := ghttp.NewServer()

		server.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/v1/catalog/service/docker-registry"),
				http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					w.Write([]byte(payload))
				}),
			),
		)

		config := backend.Config{
			FileServerURL: "http://file-server.com",
			ConsulCluster: server.URL(),
			Lifecycles: map[string]string{
				"docker": "docker_lifecycle/docker_app_lifecycle.tgz",
			},
		}

		if len(dockerRegistryURL) > 0 {
			config.DockerRegistry = &backend.DockerRegistry{
				URL:      dockerRegistryURL,
				Insecure: strings.HasPrefix(dockerRegistryURL, "http://"),
			}
		}

		logger := lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))

		return backend.NewDockerBackend(config, logger)
	}

	setupStagingRequest := func() cc_messages.StagingRequestFromCC {
		lifecycleData, err := helpers.BuildDockerStagingData("busybox")
		Ω(err).ShouldNot(HaveOccurred())

		return cc_messages.StagingRequestFromCC{
			AppId:           "bunny",
			FileDescriptors: 512,
			MemoryMB:        512,
			DiskMB:          512,
			Timeout:         512,
			LifecycleData:   lifecycleData,
		}
	}

	Context("when docker registry is running", func() {
		var (
			downloadBuilderAction models.Action
			docker                backend.Backend
			runAction             models.Action
			expectedEgressRules   []models.SecurityGroupRule
			dockerRegistryURL     string
			stagingRequest        cc_messages.StagingRequestFromCC
		)

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
			docker = setupDockerBackend(dockerRegistryURL, fmt.Sprintf(
				`[
						{"Address": "%s"},
						{"Address": "%s"}
				 ]`,
				dockerRegistryIPs[0], dockerRegistryIPs[1]))

			downloadBuilderAction = models.EmitProgressFor(
				&models.DownloadAction{
					From:     "http://file-server.com/v1/static/docker_lifecycle/docker_app_lifecycle.tgz",
					To:       "/tmp/docker_app_lifecycle",
					CacheKey: "builder-docker",
				},
				"",
				"",
				"Failed to set up docker environment",
			)

			expectedEgressRules = setupEgressRules(dockerRegistryIPs)

			stagingRequest = setupStagingRequest()
		})

		checkStagingInstructionsFunc := func() {
			desiredTask, err := docker.BuildRecipe(stagingGuid, stagingRequest)
			Ω(err).ShouldNot(HaveOccurred())

			actions := actionsFromDesiredTask(desiredTask)
			Ω(actions).Should(HaveLen(2))
			Ω(actions[0]).Should(Equal(downloadBuilderAction))
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
						},
						Env: []models.EnvironmentVariable{},
						ResourceLimits: models.ResourceLimits{
							Nofile: &fileDescriptorLimit,
						},
						Privileged: false,
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
						},
						Env: []models.EnvironmentVariable{},
						ResourceLimits: models.ResourceLimits{
							Nofile: &fileDescriptorLimit,
						},
						Privileged: false,
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
				desiredTask, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Ω(err).ShouldNot(HaveOccurred())
				Ω(desiredTask.EgressRules).Should(BeEmpty())
			})
		})

		Context("user opted-in for docker image caching", func() {
			BeforeEach(func() {
				dockerRegistryURL = fmt.Sprintf("http://%s:%d", dockerRegistryIPs[0], dockerRegistryPort)
			})

			JustBeforeEach(func() {
				cachingVar := cc_messages.EnvironmentVariable{Name: "DIEGO_DOCKER_CACHE", Value: "true"}
				stagingRequest.Environment = append(stagingRequest.Environment, cachingVar)
			})

			It("creates a cf-app-docker-staging Task with caching instructions", func() {
				desiredTask, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Ω(err).ShouldNot(HaveOccurred())

				Ω(desiredTask.Privileged).Should(BeTrue())
				Ω(desiredTask.Action).ShouldNot(BeNil())

				Ω(desiredTask.Privileged).Should(BeTrue())

				actions := actionsFromDesiredTask(desiredTask)
				Ω(actions).Should(HaveLen(2))

				runProgressAction := actions[1]
				Ω(runProgressAction).Should(BeAssignableToTypeOf(&models.EmitProgressAction{}))
				actualRunAction := runProgressAction.(*models.EmitProgressAction).Action
				Ω(actualRunAction).Should(BeAssignableToTypeOf(&models.RunAction{}))

				Ω(actualRunAction.(*models.RunAction).Args).Should(ContainElement("-cacheDockerImage"))
			})
		})
	})

	Context("when Docker Registry is not running", func() {
		var (
			docker         backend.Backend
			stagingRequest cc_messages.StagingRequestFromCC
		)

		JustBeforeEach(func() {
			dockerRegistryURL := fmt.Sprintf("http://%s:%d", dockerRegistryIPs[0], dockerRegistryPort)

			docker = setupDockerBackend(dockerRegistryURL, "[]")
			stagingRequest = setupStagingRequest()
		})

		It("errors", func() {
			_, err := docker.BuildRecipe(stagingGuid, stagingRequest)

			Ω(err).Should(HaveOccurred())
			Ω(err).Should(Equal(backend.ErrMissingDockerRegistry))
		})
	})

})
