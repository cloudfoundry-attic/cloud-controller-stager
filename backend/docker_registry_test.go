package backend_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/stager/backend"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	"github.com/pivotal-golang/lager"
)

var _ = Describe("DockerBackend", func() {
	const (
		stagingGuid        = "staging-guid"
		dockerRegistryPort = uint32(8080)
		dockerRegistryHost = "docker-registry.service.cf.internal"
	)

	var (
		dockerRegistryIPs     = []string{"10.244.2.6", "10.244.2.7"}
		dockerRegistryAddress = fmt.Sprintf("%s:%d", dockerRegistryHost, dockerRegistryPort)

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
			CCUploaderURL:          "http://cc-uploader.com",
			ConsulCluster:          server.URL(),
			DockerRegistryAddress:  dockerRegistryAddress,
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
			EgressRules: []*models.SecurityGroupRule{
				{
					Protocol:     "TCP",
					Destinations: []string{"0.0.0.0/0"},
					PortRange:    &models.PortRange{Start: 80, End: 443},
				},
			},
		}
	}

	BeforeEach(func() {
		loginServer = ""
		user = ""
		password = ""
		email = ""
	})

	Context("when docker registry is running", func() {
		var downloadBuilderAction = models.EmitProgressFor(
			&models.DownloadAction{
				From:     "http://file-server.com/v1/static/docker_lifecycle/docker_app_lifecycle.tgz",
				To:       "/tmp/docker_app_lifecycle",
				CacheKey: "docker-lifecycle",
				User:     "vcap",
			},
			"",
			"",
			"Failed to set up docker environment",
		)

		var (
			dockerBackend          backend.Backend
			expectedRunAction      models.ActionInterface
			expectedEgressRules    []*models.SecurityGroupRule
			insecureDockerRegistry bool
			stagingRequest         cc_messages.StagingRequestFromCC
		)

		BeforeEach(func() {
			insecureDockerRegistry = false
		})

		AfterEach(func() {
			expectedEgressRules = []*models.SecurityGroupRule{}
		})

		JustBeforeEach(func() {
			dockerBackend = setupDockerBackend(
				insecureDockerRegistry,
				fmt.Sprintf(
					`[
						{"Address": "%s"},
						{"Address": "%s"}
				 ]`,
					dockerRegistryIPs[0],
					dockerRegistryIPs[1],
				),
			)

			stagingRequest = setupStagingRequest()

			for i, _ := range stagingRequest.EgressRules {
				expectedEgressRules = append(expectedEgressRules, stagingRequest.EgressRules[i])
			}

			for _, ip := range dockerRegistryIPs {
				expectedEgressRules = append(expectedEgressRules, &models.SecurityGroupRule{
					Protocol:     models.TCPProtocol,
					Destinations: []string{ip},
					Ports:        []uint32{dockerRegistryPort},
				})
			}
		})

		Context("user did not opt-in for docker image caching", func() {
			It("creates a cf-app-docker-staging Task with no additional egress rules", func() {
				taskDef, _, _, err := dockerBackend.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).NotTo(HaveOccurred())
				Expect(taskDef.EgressRules).To(Equal(stagingRequest.EgressRules))
			})
		})

		Context("user opted-in for docker image caching", func() {
			var internalRunAction models.RunAction

			JustBeforeEach(func() {
				stagingRequest.Environment = append(stagingRequest.Environment, &models.EnvironmentVariable{
					Name:  "DIEGO_DOCKER_CACHE",
					Value: "true",
				})
				fileDescriptorLimit := uint64(512)
				internalRunAction = models.RunAction{
					Path: "/tmp/docker_app_lifecycle/builder",
					Args: []string{
						"-outputMetadataJSONFilename", "/tmp/docker-result/result.json",
						"-dockerRef", "busybox",
						"-cacheDockerImage",
						"-dockerRegistryHost", dockerRegistryHost,
						"-dockerRegistryPort", fmt.Sprintf("%d", dockerRegistryPort),
						"-dockerRegistryIPs", strings.Join(dockerRegistryIPs, ","),
					},
					Env: []*models.EnvironmentVariable{
						&models.EnvironmentVariable{Name: "DIEGO_DOCKER_CACHE", Value: "true"},
					},
					ResourceLimits: &models.ResourceLimits{
						Nofile: &fileDescriptorLimit,
					},
					User: "root",
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

				It("creates a cf-app-docker-staging Task with staging instructions", func() {
					taskDef, _, _, err := dockerBackend.BuildRecipe(stagingGuid, stagingRequest)
					Expect(err).NotTo(HaveOccurred())

					Expect(taskDef.Privileged).To(BeTrue())
					Expect(taskDef.Action).NotTo(BeNil())
					Expect(taskDef.EgressRules).To(Equal(expectedEgressRules))

					actions := actionsFromTaskDef(taskDef)
					Expect(actions).To(HaveLen(2))
					Expect(actions[0].GetEmitProgressAction()).To(Equal(downloadBuilderAction))
					Expect(actions[1].GetEmitProgressAction()).To(Equal(expectedRunAction))
				})
			})

			Context("and Docker Registry is insecure", func() {
				BeforeEach(func() {
					insecureDockerRegistry = true
				})

				JustBeforeEach(func() {
					internalRunAction.Args = append(internalRunAction.Args, "-insecureDockerRegistries", dockerRegistryAddress)
				})

				It("creates a cf-app-docker-staging Task with staging instructions", func() {
					taskDef, _, _, err := dockerBackend.BuildRecipe(stagingGuid, stagingRequest)
					Expect(err).NotTo(HaveOccurred())

					Expect(taskDef.Privileged).To(BeTrue())
					Expect(taskDef.Action).NotTo(BeNil())
					Expect(taskDef.EgressRules).To(Equal(expectedEgressRules))

					actions := actionsFromTaskDef(taskDef)
					Expect(actions).To(HaveLen(2))
					Expect(actions[0].GetEmitProgressAction()).To(Equal(downloadBuilderAction))
					Expect(actions[1].GetEmitProgressAction()).To(Equal(expectedRunAction))
				})
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
						"-dockerEmail", email,
					)
				})

				It("creates a cf-app-docker-staging Task with staging instructions", func() {
					taskDef, _, _, err := dockerBackend.BuildRecipe(stagingGuid, stagingRequest)
					Expect(err).NotTo(HaveOccurred())

					Expect(taskDef.Privileged).To(BeTrue())
					Expect(taskDef.Action).NotTo(BeNil())
					Expect(taskDef.EgressRules).To(Equal(expectedEgressRules))

					actions := actionsFromTaskDef(taskDef)
					Expect(actions).To(HaveLen(2))
					Expect(actions[0].GetEmitProgressAction()).To(Equal(downloadBuilderAction))
					Expect(actions[1].GetEmitProgressAction()).To(Equal(expectedRunAction))
				})
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
				stagingRequest.Environment = append(stagingRequest.Environment, &models.EnvironmentVariable{
					Name:  "DIEGO_DOCKER_CACHE",
					Value: "true",
				})
			})

			It("errors", func() {
				_, _, _, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).To(HaveOccurred())
				Expect(err).To(Equal(backend.ErrMissingDockerRegistry))
			})
		})

		Context("and user did not opt-in for docker image caching", func() {
			It("does not error", func() {
				_, _, _, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})
})
