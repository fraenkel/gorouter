package config_test

import (
	. "github.com/cloudfoundry/gorouter/config"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"time"
)

var _ = Describe("Config", func() {
	var config *Config

	BeforeEach(func() {
		config = DefaultConfig()
	})

	It("sets status config", func() {
		var b = []byte(`
status:
  port: 1234
  user: user
  pass: pass
`)

		config.Initialize(b)

		Ω(config.Status.Port).To(Equal(uint16(1234)))
		Ω(config.Status.User).To(Equal("user"))
		Ω(config.Status.Pass).To(Equal("pass"))

	})

	It("sets endpoint timeout", func() {
		var b = []byte(`
endpoint_timeout: 10
`)

		config.Initialize(b)

		Ω(config.EndpointTimeoutInSeconds).To(Equal(10))
	})

	It("sets nats config", func() {
		var b = []byte(`
nats:
  - host: remotehost
    port: 4223
    user: user
    pass: pass
`)
		config.Initialize(b)

		Ω(config.Nats).To(HaveLen(1))
		Ω(config.Nats[0].Host).To(Equal("remotehost"))
		Ω(config.Nats[0].Port).To(Equal(uint16(4223)))
		Ω(config.Nats[0].User).To(Equal("user"))
		Ω(config.Nats[0].Pass).To(Equal("pass"))
	})

	It("sets logging config", func() {
		var b = []byte(`
logging:
  file: /tmp/file
  syslog: syslog
  level: debug2
`)
		config.Initialize(b)

		Ω(config.Logging.File).To(Equal("/tmp/file"))
		Ω(config.Logging.Syslog).To(Equal("syslog"))
		Ω(config.Logging.Level).To(Equal("debug2"))
	})

	It("configures loggreggator", func() {
		var b = []byte(`
loggregatorConfig:
  url: 10.10.16.14:3456
`)

		config.Initialize(b)

		Ω(config.LoggregatorConfig.Url).To(Equal("10.10.16.14:3456"))

	})

	It("sets the rest of config", func() {
		var b = []byte(`
port: 8082
index: 1
pidfile: /tmp/pidfile
go_max_procs: 2
trace_key: "foo"
access_log: "/tmp/access_log"

publish_start_message_interval: 1
prune_stale_droplets_interval: 2
droplet_stale_threshold: 3
publish_active_apps_interval: 4
start_response_delay_interval: 15
`)

		config.Initialize(b)
		config.Process()

		Ω(config.Port).To(Equal(uint16(8082)))
		Ω(config.Index).To(Equal(uint(1)))
		Ω(config.Pidfile).To(Equal("/tmp/pidfile"))
		Ω(config.GoMaxProcs).To(Equal(2))
		Ω(config.TraceKey).To(Equal("foo"))
		Ω(config.AccessLog).To(Equal("/tmp/access_log"))

		Ω(config.PublishStartMessageIntervalInSeconds).To(Equal(1))
		Ω(config.PruneStaleDropletsInterval).To(Equal(2 * time.Second))
		Ω(config.DropletStaleThreshold).To(Equal(3 * time.Second))
		Ω(config.PublishActiveAppsInterval).To(Equal(4 * time.Second))
		Ω(config.StartResponseDelayInterval).To(Equal(15 * time.Second))
	})
})
