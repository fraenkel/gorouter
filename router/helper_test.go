package router_test

import (
	"github.com/cloudfoundry/gorouter/config"
	"github.com/cloudfoundry/gorouter/registry"
	"github.com/cloudfoundry/gorouter/test"
	. "github.com/onsi/gomega"

	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"time"
)

func SpecConfig(natsPort, statusPort, proxyPort uint16) *config.Config {
	c := config.DefaultConfig()

	c.Port = proxyPort
	c.Index = 2
	c.TraceKey = "my_trace_key"

	// Hardcode the IP to localhost to avoid leaving the machine while running tests
	c.Ip = "127.0.0.1"

	c.StartResponseDelayInterval = 10 * time.Millisecond
	c.PublishStartMessageIntervalInSeconds = 10
	c.PruneStaleDropletsInterval = 0
	c.DropletStaleThreshold = 0
	c.PublishActiveAppsInterval = 0

	c.EndpointTimeout = 500 * time.Millisecond

	c.Status = config.StatusConfig{
		Port: statusPort,
		User: "user",
		Pass: "pass",
	}

	c.Nats = []config.NatsConfig{
		{
			Host: "localhost",
			Port: natsPort,
			User: "nats",
			Pass: "nats",
		},
	}

	c.Logging = config.LoggingConfig{
		File:  "/dev/stderr",
		Level: "info",
	}

	return c
}

func StartNats(port uint16, wait bool) *exec.Cmd {
	cmd := exec.Command("gnatsd", "-p", strconv.Itoa(int(port)), "--user", "nats", "--pass", "nats")
	err := cmd.Start()
	Ω(err).ShouldNot(HaveOccurred())

	if wait {
		err = waitUntilNatsUp(port)
		Ω(err).ShouldNot(HaveOccurred())
	}

	return cmd
}

func StopNats(port uint16, cmd *exec.Cmd) {
	cmd.Process.Kill()
	cmd.Wait()

	if port != 0 {
		err := waitUntilNatsDown(port)
		Ω(err).ShouldNot(HaveOccurred())
	}
}

func nextAvailPort() uint16 {
	listener, err := net.Listen("tcp", ":0")
	Ω(err).ShouldNot(HaveOccurred())

	defer listener.Close()

	_, portStr, err := net.SplitHostPort(listener.Addr().String())
	Ω(err).ShouldNot(HaveOccurred())

	port, err := strconv.Atoi(portStr)
	Ω(err).ShouldNot(HaveOccurred())

	return uint16(port)
}

func waitUntilNatsUp(port uint16) error {
	maxWait := 10
	for i := 0; i < maxWait; i++ {
		time.Sleep(500 * time.Millisecond)
		_, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return nil
		}
	}

	return errors.New("Waited too long for NATS to start")
}

func waitUntilNatsDown(port uint16) error {
	maxWait := 10
	for i := 0; i < maxWait; i++ {
		time.Sleep(500 * time.Millisecond)
		_, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return nil
		}
	}

	return errors.New("Waited too long for NATS to stop")
}

func waitMsgReceived(registry *registry.CFRegistry, app *test.TestApp, expectedToBeFound bool, timeout time.Duration) bool {
	interval := time.Millisecond * 50
	repetitions := int(timeout / interval)

	for j := 0; j < repetitions; j++ {
		received := true
		for _, url := range app.Urls() {
			_, ok := registry.Lookup(url)
			if ok != expectedToBeFound {
				received = false
				break
			}
		}
		if received {
			return true
		}
		time.Sleep(interval)
	}

	return false
}

func waitAppRegistered(registry *registry.CFRegistry, app *test.TestApp, timeout time.Duration) bool {
	return waitMsgReceived(registry, app, true, timeout)
}

func waitAppUnregistered(registry *registry.CFRegistry, app *test.TestApp, timeout time.Duration) bool {
	return waitMsgReceived(registry, app, false, timeout)
}

func timeoutDialler() func(net, addr string) (c net.Conn, err error) {
	return func(netw, addr string) (net.Conn, error) {
		c, err := net.DialTimeout(netw, addr, 10*time.Second)
		c.SetDeadline(time.Now().Add(2 * time.Second))
		return c, err
	}
}
