package testrunner

import (
	"os/exec"
	"strings"
	"time"

	"github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

type StagerRunner struct {
	stagerBin     string
	stagerAddr    string
	etcdCluster   []string
	natsAddresses []string
	diegoAPIURL   string
	ccBaseURL     string

	session     *gexec.Session
	CompilerUrl string
}

type Config struct {
	StagerBin     string
	StagerAddr    string
	EtcdCluster   []string
	NatsAddresses []string
	DiegoAPIURL   string
	CCBaseURL     string
}

func New(config Config) *StagerRunner {
	return &StagerRunner{
		stagerBin:     config.StagerBin,
		stagerAddr:    config.StagerAddr,
		etcdCluster:   config.EtcdCluster,
		natsAddresses: config.NatsAddresses,
		diegoAPIURL:   config.DiegoAPIURL,
		ccBaseURL:     config.CCBaseURL,
	}
}

func (r *StagerRunner) Start(args ...string) {
	if r.session != nil {
		panic("starting more than one stager runner!!!")
	}

	stagerSession, err := gexec.Start(
		exec.Command(
			r.stagerBin,
			append([]string{
				"-etcdCluster", strings.Join(r.etcdCluster, ","),
				"-natsAddresses", strings.Join(r.natsAddresses, ","),
				"-diegoAPIURL", r.diegoAPIURL,
				"-listenAddr", r.stagerAddr,
				"-ccBaseURL", r.ccBaseURL,
			}, args...)...,
		),
		gexec.NewPrefixedWriter("\x1b[32m[o]\x1b[95m[stager]\x1b[0m ", ginkgo.GinkgoWriter),
		gexec.NewPrefixedWriter("\x1b[91m[e]\x1b[95m[stager]\x1b[0m ", ginkgo.GinkgoWriter),
	)

	Ω(err).ShouldNot(HaveOccurred())
	Eventually(stagerSession).Should(gbytes.Say("Listening for staging requests!"))

	r.session = stagerSession
}

func (r *StagerRunner) Stop() {
	if r.session != nil {
		r.session.Interrupt().Wait(5 * time.Second)
		r.session = nil
	}
}

func (r *StagerRunner) KillWithFire() {
	if r.session != nil {
		r.session.Kill().Wait(5 * time.Second)
		r.session = nil
	}
}

func (r *StagerRunner) Session() *gexec.Session {
	return r.session
}
