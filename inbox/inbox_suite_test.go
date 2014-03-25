package inbox_test

import (
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/cloudfoundry/gosteno"
)

func TestInbox(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Inbox Suite")
}

var _ = BeforeEach(func() {
	gosteno.EnterTestMode()
})
