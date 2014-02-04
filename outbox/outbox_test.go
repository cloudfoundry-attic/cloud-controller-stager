package outbox_test

import (
	"errors"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	. "github.com/cloudfoundry-incubator/stager/outbox"
	steno "github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/storeadapter"
	"github.com/cloudfoundry/storeadapter/fakestoreadapter"
	"github.com/cloudfoundry/yagnats"
	"github.com/cloudfoundry/yagnats/fakeyagnats"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"time"
)

var _ = Describe("Outbox", func() {
	var fakenats *fakeyagnats.FakeYagnats
	var testingSink *steno.TestingSink
	var fauxStoreAdapter *fakestoreadapter.FakeStoreAdapter
	var logger *steno.Logger

	var stagerBBS bbs.StagerBBS
	var executorBBS bbs.ExecutorBBS

	BeforeEach(func() {
		fauxStoreAdapter = fakestoreadapter.New()

		testingSink = steno.NewTestingSink()
		stenoConfig := &steno.Config{
			Sinks: []steno.Sink{testingSink},
		}
		steno.Init(stenoConfig)

		fakenats = fakeyagnats.New()
		logger = steno.NewLogger("fakelogger")

		stagerBBS = bbs.New(fauxStoreAdapter)
		executorBBS = bbs.New(fauxStoreAdapter)
	})

	JustBeforeEach(func() {
		go Listen(stagerBBS, fakenats, logger)
	})

	Context("when a completed RunOnce appears in the outbox", func() {
		runOnce := models.RunOnce{
			Guid:    "some-task-id",
			ReplyTo: "some-requester",
		}

		BeforeEach(func() {
			stagerBBS.DesireRunOnce(runOnce)
		})

		It("publishes to ReplyTo and then marks the RunOnce as completed", func(done Done) {
			events := make(chan string)

			fakenats.Subscribe("some-requester", func(*yagnats.Message) {
				time.Sleep(100 * time.Millisecond)
				events <- "published"
			})

			go func() {
				watchEvents, _, _ := fauxStoreAdapter.Watch("/")

				for {
					ev := <-watchEvents
					if ev.Type == storeadapter.DeleteEvent {
						events <- "deleted"
					}
				}
			}()

			executorBBS.CompleteRunOnce(runOnce)

			Expect(<-events).To(Equal("published"))
			Expect(<-events).To(Equal("deleted"))

			close(done)
		}, 5.0)
	})

	Context("when an error is seen while watching", func() {
		It("starts watching again", func(done Done) {
			calledBack := make(chan bool)

			fakenats.Subscribe("some-requester", func(*yagnats.Message) {
				calledBack <- true
			})

			fakenats.Subscribe("some-other-requester", func(*yagnats.Message) {
				calledBack <- true
			})

			err := executorBBS.CompleteRunOnce(models.RunOnce{
				Guid:    "some-task-id",
				ReplyTo: "some-requester",
			})
			Ω(err).ShouldNot(HaveOccurred())

			<-calledBack

			fauxStoreAdapter.WatchErrChannel <- errors.New("oh no!")

			// wait for watcher to sleep and then re-watch
			time.Sleep(1 * time.Second)

			err = executorBBS.CompleteRunOnce(models.RunOnce{
				Guid:    "some-other-task-id",
				ReplyTo: "some-other-requester",
			})
			Ω(err).ShouldNot(HaveOccurred())

			<-calledBack

			close(done)
		}, 5.0)
	})
})
