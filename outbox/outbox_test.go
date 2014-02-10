package outbox_test

import (
	"errors"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs/fakebbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	. "github.com/cloudfoundry-incubator/stager/outbox"
	steno "github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/storeadapter"
	"github.com/cloudfoundry/yagnats"
	"github.com/cloudfoundry/yagnats/fakeyagnats"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Outbox", func() {
	var fakenats *fakeyagnats.FakeYagnats
	var adapter storeadapter.StoreAdapter
	var logger *steno.Logger

	var bbs *fakebbs.FakeStagerBBS

	BeforeEach(func() {
		adapter = etcdRunner.Adapter()

		fakenats = fakeyagnats.New()
		logger = steno.NewLogger("fakelogger")

		bbs = fakebbs.NewFakeStagerBBS()
	})

	JustBeforeEach(func() {
		go Listen(bbs, fakenats, logger)
		<-bbs.CalledCompletedRunOnce
	})

	Context("when a completed RunOnce appears in the outbox", func() {
		runOnce := models.RunOnce{
			Guid:    "some-task-id",
			ReplyTo: "some-requester",
		}

		It("publishes to ReplyTo and then marks the RunOnce as completed", func(done Done) {
			published := make(chan bool)

			fakenats.Subscribe("some-requester", func(*yagnats.Message) {
				published <- true
			})

			bbs.CompletedRunOnceChan <- runOnce

			Expect(<-published).To(BeTrue())
			Ω(bbs.ResolvedRunOnce).Should(Equal(runOnce))

			close(done)
		}, 5.0)

		Context("when the RunOnce fails to resolve", func() {
			It("should not send a response to the requester", func(done Done) {
				published := make(chan bool)
				fakenats.Subscribe("some-requester", func(*yagnats.Message) {
					published <- true
				})

				bbs.ResolveRunOnceErr = errors.New("oops")
				bbs.CompletedRunOnceChan <- runOnce
				Consistently(published).ShouldNot(Receive())
				close(done)
			}, 5.0)
		})
	})

	Context("when an error is seen while watching", func() {
		It("starts watching again", func(done Done) {
			calledBack := make(chan bool)

			fakenats.Subscribe("requester", func(*yagnats.Message) {
				calledBack <- true
			})

			bbs.CompletedRunOnceErrChan <- errors.New("hell")

			<-bbs.CalledCompletedRunOnce

			runOnce := models.RunOnce{
				Guid:    "some-other-task-id",
				ReplyTo: "requester",
			}

			bbs.CompletedRunOnceChan <- runOnce
			<-calledBack
			Ω(bbs.ResolvedRunOnce).Should(Equal(runOnce))

			close(done)
		}, 2.0)
	})
})
