package outbox_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/cloudfoundry/gosteno"
	"testing"
)

func TestOutbox(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Outbox Suite")
}

var _ = BeforeEach(func() {
	gosteno.EnterTestMode()
})
