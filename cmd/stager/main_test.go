package main_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages/flags"
	"github.com/cloudfoundry-incubator/stager"
	"github.com/cloudfoundry-incubator/stager/cmd/stager/testrunner"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
	"github.com/tedsuo/rata"
)

var _ = Describe("Stager", func() {
	var (
		fakeReceptor *ghttp.Server
		fakeCC       *ghttp.Server

		requestGenerator *rata.RequestGenerator
		httpClient       *http.Client

		callbackURL string
	)

	BeforeEach(func() {
		stagerPort := 8888 + GinkgoParallelNode()
		stagerURL := fmt.Sprintf("http://127.0.0.1:%d", stagerPort)
		callbackURL = stagerURL + "/v1/staging/my-task-guid/completed"

		fakeReceptor = ghttp.NewServer()
		fakeCC = ghttp.NewServer()

		runner = testrunner.New(testrunner.Config{
			StagerBin:          stagerPath,
			StagerURL:          stagerURL,
			DiegoAPIURL:        fakeReceptor.URL(),
			CCBaseURL:          fakeCC.URL(),
			DockerStagingStack: "docker-staging-stack",
		})

		requestGenerator = rata.NewRequestGenerator(stagerURL, stager.Routes)
		httpClient = http.DefaultClient
	})

	AfterEach(func() {
		runner.Stop()
	})

	Context("when started", func() {
		BeforeEach(func() {
			runner.Start(
				"-lifecycle", "buildpack/linux:lifecycle.zip",
				"-lifecycle", "docker:docker/lifecycle.tgz",
			)
			Eventually(runner.Session()).Should(gbytes.Say("Listening for staging requests!"))
		})

		Describe("when a buildpack staging request is received", func() {
			It("desires a staging task via the API", func() {
				fakeReceptor.RouteToHandler("POST", "/v1/tasks", func(w http.ResponseWriter, req *http.Request) {
					var taskRequest receptor.TaskCreateRequest
					err := json.NewDecoder(req.Body).Decode(&taskRequest)
					Expect(err).NotTo(HaveOccurred())

					Expect(taskRequest.MemoryMB).To(Equal(1024))
					Expect(taskRequest.DiskMB).To(Equal(128))
					Expect(taskRequest.CompletionCallbackURL).To(Equal(callbackURL))
				})

				req, err := requestGenerator.CreateRequest(stager.StageRoute, rata.Params{"staging_guid": "my-task-guid"}, strings.NewReader(`{
					"app_id":"my-app-guid",
					"file_descriptors":3,
					"memory_mb" : 1024,
					"disk_mb" : 128,
					"environment" : [],
					"lifecycle": "buildpack",
					"lifecycle_data": {
					  "buildpacks" : [],
						"stack":"linux",
					  "app_bits_download_uri":"http://example.com/app_bits"
					}
				}`))
				Expect(err).NotTo(HaveOccurred())
				req.Header.Set("Content-Type", "application/json")

				resp, err := httpClient.Do(req)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusAccepted))

				Eventually(fakeReceptor.ReceivedRequests).Should(HaveLen(1))
				Consistently(runner.Session()).ShouldNot(gexec.Exit())
			})
		})

		Describe("when a docker staging request is received", func() {
			It("desires a staging task via the API", func() {
				fakeReceptor.RouteToHandler("POST", "/v1/tasks", func(w http.ResponseWriter, req *http.Request) {
					var taskRequest receptor.TaskCreateRequest
					err := json.NewDecoder(req.Body).Decode(&taskRequest)
					Expect(err).NotTo(HaveOccurred())

					Expect(taskRequest.MemoryMB).To(Equal(1024))
					Expect(taskRequest.DiskMB).To(Equal(128))
					Expect(taskRequest.CompletionCallbackURL).To(Equal(callbackURL))
				})

				req, err := requestGenerator.CreateRequest(stager.StageRoute, rata.Params{"staging_guid": "my-task-guid"}, strings.NewReader(`{
					"app_id":"my-app-guid",
					"file_descriptors":3,
					"memory_mb" : 1024,
					"disk_mb" : 128,
					"environment" : [],
					"lifecycle": "docker",
					"lifecycle_data": {
					  "docker_image":"http://docker.docker/docker"
					}
				}`))
				Expect(err).NotTo(HaveOccurred())
				req.Header.Set("Content-Type", "application/json")

				resp, err := httpClient.Do(req)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusAccepted))

				Eventually(fakeReceptor.ReceivedRequests).Should(HaveLen(1))
				Consistently(runner.Session()).ShouldNot(gexec.Exit())
			})
		})

		Describe("when a stop staging request is recevied", func() {
			BeforeEach(func() {
				task := receptor.TaskResponse{
					TaskGuid:   "the-task-guid",
					Annotation: `{"lifecycle": "whatever"}`,
				}

				fakeReceptor.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/v1/tasks/the-task-guid"),
						ghttp.RespondWithJSONEncoded(http.StatusOK, task),
					),
				)
				fakeReceptor.AppendHandlers(
					ghttp.VerifyRequest("POST", "/v1/tasks/the-task-guid/cancel"),
				)
			})

			It("cancels the staging task via the API", func() {
				req, err := requestGenerator.CreateRequest(stager.StopStagingRoute, rata.Params{"staging_guid": "the-task-guid"}, nil)
				Expect(err).NotTo(HaveOccurred())
				req.Header.Set("Content-Type", "application/json")

				resp, err := httpClient.Do(req)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusAccepted))

				Eventually(fakeReceptor.ReceivedRequests).Should(HaveLen(2))
				Consistently(runner.Session()).ShouldNot(gexec.Exit())
			})
		})

		Describe("when a staging task completes", func() {
			Context("for a docker lifecycle", func() {
				BeforeEach(func() {
					fakeCC.AppendHandlers(
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("POST", "/internal/staging/the-task-guid/completed"),
							ghttp.VerifyContentType("application/json"),
							ghttp.VerifyJSON(`{
								"execution_metadata": "metadata",
								"detected_start_command": {"a": "b"},
								"lifecycle_data": {"docker_image": "http://docker.docker/docker"}
							}`),
						),
					)

					taskJSON, err := json.Marshal(receptor.TaskResponse{
						TaskGuid: "the-task-guid",
						Action: models.WrapAction(&models.RunAction{
							User: "me",
							Path: "ls",
						}),
						Domain: cc_messages.StagingTaskDomain,
						Annotation: `{
							"lifecycle": "docker"
						}`,
						Result: `{
							"execution_metadata": "metadata",
							"detected_start_command": {"a": "b"},
							"docker_image": "http://docker.docker/docker"
						}`,
					})
					Expect(err).NotTo(HaveOccurred())

					req, err := requestGenerator.CreateRequest(stager.StagingCompletedRoute, rata.Params{"staging_guid": "the-task-guid"}, bytes.NewReader(taskJSON))
					Expect(err).NotTo(HaveOccurred())

					req.Header.Set("Content-Type", "application/json")

					resp, err := httpClient.Do(req)
					Expect(err).NotTo(HaveOccurred())
					Expect(resp.StatusCode).To(Equal(http.StatusOK))
				})

				It("POSTs to the CC that staging is complete", func() {
					Eventually(fakeCC.ReceivedRequests).Should(HaveLen(1))
				})
			})

			Context("for a buildpack lifecycle", func() {
				BeforeEach(func() {
					fakeCC.AppendHandlers(
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("POST", "/internal/staging/the-task-guid/completed"),
							ghttp.VerifyContentType("application/json"),
							ghttp.VerifyJSON(`{
								"execution_metadata": "metadata",
								"detected_start_command": {"a": "b"},
								"lifecycle_data": {
									"buildpack_key": "buildpack-key",
									"detected_buildpack": "detected-buildpack"
								}
							}`),
						),
					)

					taskJSON, err := json.Marshal(receptor.TaskResponse{
						TaskGuid: "the-task-guid",
						Action: models.WrapAction(&models.RunAction{
							User: "me",
							Path: "ls",
						}),
						Domain: cc_messages.StagingTaskDomain,
						Annotation: `{
							"lifecycle": "buildpack"
						}`,
						Result: `{
							"buildpack_key": "buildpack-key",
							"detected_buildpack": "detected-buildpack",
							"execution_metadata": "metadata",
							"detected_start_command": {"a": "b"}
						}`,
					})
					Expect(err).NotTo(HaveOccurred())

					req, err := requestGenerator.CreateRequest(stager.StagingCompletedRoute, rata.Params{"staging_guid": "the-task-guid"}, bytes.NewReader(taskJSON))
					Expect(err).NotTo(HaveOccurred())

					req.Header.Set("Content-Type", "application/json")

					resp, err := httpClient.Do(req)
					Expect(err).NotTo(HaveOccurred())
					Expect(resp.StatusCode).To(Equal(http.StatusOK))
				})

				It("POSTs to the CC that staging is complete", func() {
					Eventually(fakeCC.ReceivedRequests).Should(HaveLen(1))
				})
			})
		})
	})

	Describe("-insecureDockerRegistry arg", func() {
		Context("when started with -insecureDockerRegistry arg", func() {
			BeforeEach(func() {
				runner.Start("-lifecycle", "linux:lifecycle.zip", "-insecureDockerRegistry")
				Eventually(runner.Session()).Should(gbytes.Say("Listening for staging requests!"))
			})

			It("starts successfully", func() {
				Consistently(runner.Session()).ShouldNot(gexec.Exit())
			})
		})
	})

	Describe("-consulCluster arg", func() {
		Context("when started with a valid -consulCluster arg", func() {
			BeforeEach(func() {
				runner.Start("-lifecycle", "linux:lifecycle.zip",
					"-consulCluster", "http://localhost:8500")
				Eventually(runner.Session()).Should(gbytes.Say("Listening for staging requests!"))
			})

			It("starts successfully", func() {
				Consistently(runner.Session()).ShouldNot(gexec.Exit())
			})
		})

		Context("when started with an invalid -consulCluster arg", func() {
			BeforeEach(func() {
				runner.Start("-lifecycle", "linux:lifecycle.zip",
					"-consulCluster", "://noscheme:8500")
			})

			It("logs and errors", func() {
				Eventually(runner.Session().ExitCode()).ShouldNot(Equal(0))
				Eventually(runner.Session()).Should(gbytes.Say("Error parsing consul agent URL"))
			})
		})
	})

	Describe("-dockerRegistryAddress arg", func() {
		Context("when started with a valid -dockerRegistryAddress arg", func() {
			BeforeEach(func() {
				runner.Start("-lifecycle", "linux:lifecycle.zip",
					"-dockerRegistryAddress", "docker-registry.service.cf.internal:8080")
				Eventually(runner.Session()).Should(gbytes.Say("Listening for staging requests!"))
			})

			It("starts successfully", func() {
				Consistently(runner.Session()).ShouldNot(gexec.Exit())
			})
		})

		Context("when started with an invalid -dockerRegistryAddress arg", func() {
			BeforeEach(func() {
				runner.Start("-lifecycle", "linux:lifecycle.zip",
					"-dockerRegistryAddress", "://noscheme:8500")
			})

			It("logs and errors", func() {
				Eventually(runner.Session().ExitCode()).ShouldNot(Equal(0))
				Eventually(runner.Session()).Should(gbytes.Say("Error parsing Docker Registry address"))
			})
		})
	})

	Describe("-lifecycles arg", func() {
		Context("when started with an invalid -lifecycles arg", func() {
			BeforeEach(func() {
				runner.Start("-lifecycle", "invalid form")
			})

			It("logs and errors", func() {
				Eventually(runner.Session().ExitCode()).ShouldNot(Equal(0))
				Eventually(runner.Session().Err).Should(gbytes.Say(flags.ErrLifecycleFormatInvalid.Error()))
			})
		})
	})

	Describe("-stagerURL arg", func() {
		Context("when started with an invalid -stagerURL arg", func() {
			BeforeEach(func() {
				runner.Start("-stagerURL", `://localhost:8080`)
			})

			It("logs and errors", func() {
				Eventually(runner.Session().ExitCode()).ShouldNot(Equal(0))
				Eventually(runner.Session()).Should(gbytes.Say("Invalid stager URL"))
			})
		})
	})

})
