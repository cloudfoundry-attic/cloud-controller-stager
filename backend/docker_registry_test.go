package backend_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/stager/backend"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	"github.com/pivotal-golang/lager"
)

var _ = Describe("DockerBackend", func() {

	const stagingGuid = "staging-guid"
	const dockerRegistryPort = uint16(8080)
	var (
		dockerRegistryIPs = []string{"10.244.2.6", "10.244.2.7"}

		loginServer string
		user        string
		password    string
		email       string
	)

	setupDockerBackend := func(insecureDockerRegistry bool, payload string) backend.Backend {
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
			FileServerURL:          "http://file-server.com",
			ConsulCluster:          server.URL(),
			InsecureDockerRegistry: insecureDockerRegistry,
			Lifecycles: map[string]string{
				"docker": "docker_lifecycle/docker_app_lifecycle.tgz",
			},
		}

		logger := lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))

		return backend.NewDockerBackend(config, logger)
	}

	setupStagingRequest := func() cc_messages.StagingRequestFromCC {
		rawJsonBytes, err := json.Marshal(cc_messages.DockerStagingData{
			DockerImageUrl:    "busybox",
			DockerLoginServer: loginServer,
			DockerUser:        user,
			DockerPassword:    password,
			DockerEmail:       email,
		})
		Expect(err).NotTo(HaveOccurred())
		lifecycleData := json.RawMessage(rawJsonBytes)

		Expect(err).NotTo(HaveOccurred())

		return cc_messages.StagingRequestFromCC{
			AppId:           "bunny",
			FileDescriptors: 512,
			MemoryMB:        512,
			DiskMB:          512,
			Timeout:         512,
			LifecycleData:   &lifecycleData,
		}
	}

	BeforeEach(func() {
		loginServer = ""
		user = ""
		password = ""
		email = ""
	})

	Context("when docker registry is running", func() {
		var (
			downloadBuilderAction  models.Action
			docker                 backend.Backend
			expectedRunAction      models.Action
			expectedEgressRules    []models.SecurityGroupRule
			insecureDockerRegistry bool
			stagingRequest         cc_messages.StagingRequestFromCC
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

		BeforeEach(func() {
			insecureDockerRegistry = false
		})

		JustBeforeEach(func() {
			docker = setupDockerBackend(insecureDockerRegistry, fmt.Sprintf(
				`[
						{"Address": "%s"},
						{"Address": "%s"}
				 ]`,
				dockerRegistryIPs[0], dockerRegistryIPs[1]))

			downloadBuilderAction = models.EmitProgressFor(
				&models.DownloadAction{
					From:     "http://file-server.com/v1/static/docker_lifecycle/docker_app_lifecycle.tgz",
					To:       "/tmp/docker_app_lifecycle",
					CacheKey: "docker-lifecycle",
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
			Expect(err).NotTo(HaveOccurred())

			Expect(desiredTask.Privileged).To(BeTrue())
			Expect(desiredTask.Action).NotTo(BeNil())
			Expect(desiredTask.EgressRules).To(ConsistOf(expectedEgressRules))

			actions := actionsFromDesiredTask(desiredTask)
			Expect(actions).To(HaveLen(2))
			Expect(actions[0]).To(Equal(downloadBuilderAction))
			Expect(actions[1]).To(Equal(expectedRunAction))
		}

		Context("user did not opt-in for docker image caching", func() {
			It("creates a cf-app-docker-staging Task with no additional egress rules", func() {
				desiredTask, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).NotTo(HaveOccurred())
				Expect(desiredTask.EgressRules).To(BeEmpty())
			})
		})

		Context("user opted-in for docker image caching", func() {
			modelsCachingVar := models.EnvironmentVariable{Name: "DIEGO_DOCKER_CACHE", Value: "true"}
			var (
				internalRunAction models.RunAction
				dockerRegistries  string
			)

			JustBeforeEach(func() {
				cachingVar := cc_messages.EnvironmentVariable{Name: "DIEGO_DOCKER_CACHE", Value: "true"}
				stagingRequest.Environment = append(stagingRequest.Environment, cachingVar)
				fileDescriptorLimit := uint64(512)
				dockerRegistries = setupDockerRegistries(dockerRegistryIPs, dockerRegistryPort)
				internalRunAction = models.RunAction{
					Path: "/tmp/docker_app_lifecycle/builder",
					Args: []string{
						"-outputMetadataJSONFilename",
						"/tmp/docker-result/result.json",
						"-dockerRef",
						"busybox",
						"-cacheDockerImage",
						"-dockerRegistryAddresses",
						dockerRegistries,
					},
					Env: []models.EnvironmentVariable{modelsCachingVar},
					ResourceLimits: models.ResourceLimits{
						Nofile: &fileDescriptorLimit,
					},
					Privileged: true,
				}
				expectedRunAction = models.EmitProgressFor(
					&internalRunAction,
					"Staging...",
					"Staging Complete",
					"Staging Failed",
				)
			})

			Context("and Docker Registry is secure", func() {
				BeforeEach(func() {
					insecureDockerRegistry = false
				})

				It("creates a cf-app-docker-staging Task with staging instructions", checkStagingInstructionsFunc)
			})

			Context("and Docker Registry is insecure", func() {
				BeforeEach(func() {
					insecureDockerRegistry = true
				})

				JustBeforeEach(func() {
					internalRunAction.Args = append(internalRunAction.Args, "-insecureDockerRegistries", dockerRegistries)
				})

				It("creates a cf-app-docker-staging Task with staging instructions", checkStagingInstructionsFunc)
			})

			Context("and credentials are provided", func() {
				BeforeEach(func() {
					loginServer = "http://loginServer.com"
					user = "user"
					password = "password"
					email = "email@example.com"
				})

				JustBeforeEach(func() {
					internalRunAction.Args = append(internalRunAction.Args,
						"-dockerLoginServer", loginServer,
						"-dockerUser", user,
						"-dockerPassword", password,
						"-dockerEmail", email)
				})

				It("creates a cf-app-docker-staging Task with staging instructions", checkStagingInstructionsFunc)
			})

		})
	})

	Context("when Docker Registry is not running", func() {
		var (
			docker         backend.Backend
			stagingRequest cc_messages.StagingRequestFromCC
		)

		BeforeEach(func() {
			docker = setupDockerBackend(true, "[]")
			stagingRequest = setupStagingRequest()
		})

		Context("and user opted-in for docker image caching", func() {
			BeforeEach(func() {
				cachingVar := cc_messages.EnvironmentVariable{Name: "DIEGO_DOCKER_CACHE", Value: "true"}
				stagingRequest.Environment = append(stagingRequest.Environment, cachingVar)
			})

			It("errors", func() {
				_, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).To(HaveOccurred())
				Expect(err).To(Equal(backend.ErrMissingDockerRegistry))
			})
		})

		Context("and user did not opt-in for docker image caching", func() {
			It("does not error", func() {
				_, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).NotTo(HaveOccurred())
			})
		})

	})

})
