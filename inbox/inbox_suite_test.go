package inbox_test

import (
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func TestInbox(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Inbox Suite")
}
