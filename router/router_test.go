package router_test

import (
	"github.com/cloudfoundry/gorouter/common"
	"github.com/cloudfoundry/gorouter/config"
	"github.com/cloudfoundry/gorouter/proxy"
	rregistry "github.com/cloudfoundry/gorouter/registry"
	"github.com/cloudfoundry/gorouter/route"
	. "github.com/cloudfoundry/gorouter/router"
	"github.com/cloudfoundry/gorouter/test"
	vvarz "github.com/cloudfoundry/gorouter/varz"
	"github.com/cloudfoundry/yagnats"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

var _ = Describe("Router", func() {
	var Config *config.Config
	var natsServerCmd *exec.Cmd
	var mbusClient *yagnats.Client
	var registry *rregistry.CFRegistry
	var varz vvarz.Varz
	var router *Router
	var natsPort uint16

	BeforeEach(func() {
		natsPort = nextAvailPort()

		natsServerCmd = StartNats(natsPort, true)

		proxyPort := nextAvailPort()
		statusPort := nextAvailPort()

		Config = SpecConfig(natsPort, statusPort, proxyPort)

		mbusClient = yagnats.NewClient()
		registry = rregistry.NewCFRegistry(Config, mbusClient)
		varz = vvarz.NewVarz(registry)
		router, err := NewRouter(Config, mbusClient, registry, varz)
		Ω(err).ShouldNot(HaveOccurred())
		router.Run()

		err = waitUntilNatsUp(natsPort)
		Ω(err).ShouldNot(HaveOccurred())
	})

	AfterEach(func() {
		StopNats(natsPort, natsServerCmd)
	})

	It("RouterGreets", func() {
		response := make(chan []byte)

		mbusClient.Subscribe("router.greet.test.response", func(msg *yagnats.Message) {
			response <- msg.Payload
		})

		mbusClient.PublishWithReplyTo("router.greet", "router.greet.test.response", []byte{})

		var msg []byte
		Eventually(response, 1).Should(Receive(&msg))
		Ω(string(msg)).To(MatchRegexp(".*\"minimumRegisterIntervalInSeconds\":5.*"))
	})

	It("discovers", func() {
		// Test if router responses to discover message
		sig := make(chan common.VcapComponent)

		// Since the form of uptime is xxd:xxh:xxm:xxs, we should make
		// sure that router has run at least for one second
		time.Sleep(time.Second)

		mbusClient.Subscribe("vcap.component.discover.test.response", func(msg *yagnats.Message) {
			var component common.VcapComponent
			_ = json.Unmarshal(msg.Payload, &component)
			sig <- component
		})

		mbusClient.PublishWithReplyTo(
			"vcap.component.discover",
			"vcap.component.discover.test.response",
			[]byte{},
		)

		var cc common.VcapComponent
		Eventually(sig).Should(Receive(&cc))

		var emptyTime time.Time
		var emptyDuration common.Duration

		Ω(cc.Type).To(Equal("Router"))
		Ω(cc.Index).To(Equal(uint(2)))
		Ω(cc.UUID).ToNot(Equal(""))
		Ω(cc.Start).ToNot(Equal(emptyTime))
		Ω(cc.Uptime).ToNot(Equal(emptyDuration))

		verify_var_z(cc.Host, cc.Credentials[0], cc.Credentials[1])
		verify_health_z(cc.Host, registry)
	})

	It("registers and unregisters", func() {
		app := test.NewGreetApp([]route.Uri{"test.vcap.me"}, Config.Port, mbusClient, nil)
		app.Listen()
		Ω(waitAppRegistered(registry, app, time.Second*5)).To(BeTrue())

		app.VerifyAppStatus(200)

		app.Unregister()
		Ω(waitAppUnregistered(registry, app, time.Second*5)).To(BeTrue())
		app.VerifyAppStatus(404)
	})

	It("registry contains last updated varz", func() {
		app1 := test.NewGreetApp([]route.Uri{"test1.vcap.me"}, Config.Port, mbusClient, nil)
		app1.Listen()
		Ω(waitAppRegistered(registry, app1, time.Second*1)).To(BeTrue())

		time.Sleep(2 * time.Second)
		initialUpdateTime := fetchRecursively(readVarz(varz), "ms_since_last_registry_update").(float64)
		// initialUpdateTime should be roughly 2 seconds.

		app2 := test.NewGreetApp([]route.Uri{"test2.vcap.me"}, Config.Port, mbusClient, nil)
		app2.Listen()
		Ω(waitAppRegistered(registry, app2, time.Second*1)).To(BeTrue())

		// updateTime should be roughly 0 seconds
		updateTime := fetchRecursively(readVarz(varz), "ms_since_last_registry_update").(float64)
		Ω(updateTime).To(BeNumerically("<", initialUpdateTime))
	})

	It("varz", func() {
		app := test.NewGreetApp([]route.Uri{"count.vcap.me"}, Config.Port, mbusClient, map[string]string{"framework": "rails"})
		app.Listen()
		additionalRequests := 100
		go app.RegisterRepeatedly(100 * time.Millisecond)
		Ω(waitAppRegistered(registry, app, time.Millisecond*500)).To(BeTrue())
		// Send seed request
		sendRequests("count.vcap.me", Config.Port, 1)
		initial_varz := readVarz(varz)

		// Send requests
		sendRequests("count.vcap.me", Config.Port, additionalRequests)
		updated_varz := readVarz(varz)

		// Verify varz update
		initialRequestCount := fetchRecursively(initial_varz, "requests").(float64)
		updatedRequestCount := fetchRecursively(updated_varz, "requests").(float64)
		requestCount := int(updatedRequestCount - initialRequestCount)
		Ω(requestCount).To(Equal(additionalRequests))

		initialResponse2xxCount := fetchRecursively(initial_varz, "responses_2xx").(float64)
		updatedResponse2xxCount := fetchRecursively(updated_varz, "responses_2xx").(float64)
		response2xxCount := int(updatedResponse2xxCount - initialResponse2xxCount)
		Ω(response2xxCount).To(Equal(additionalRequests))

		app.Unregister()
	})

	It("sticky session", func() {
		apps := make([]*test.TestApp, 10)
		for i := range apps {
			apps[i] = test.NewStickyApp([]route.Uri{"sticky.vcap.me"}, Config.Port, mbusClient, nil)
			apps[i].Listen()
		}

		for _, app := range apps {
			Ω(waitAppRegistered(registry, app, time.Millisecond*500)).To(BeTrue())
		}
		sessionCookie, vcapCookie, port1 := getSessionAndAppPort("sticky.vcap.me", Config.Port)
		port2 := getAppPortWithSticky("sticky.vcap.me", Config.Port, sessionCookie, vcapCookie)

		Ω(port1).To(Equal(port2))
		Ω(vcapCookie.Path).To(Equal("/"))

		for _, app := range apps {
			app.Unregister()
		}
	})

	Context("Run", func() {
		It("fails", func() {
			Ω(func() { router.Run() }).To(Panic())
		})
	})

	It("handles a PUT request", func() {
		app := test.NewTestApp([]route.Uri{"greet.vcap.me"}, Config.Port, mbusClient, nil)

		var rr *http.Request
		var msg string
		app.AddHandler("/", func(w http.ResponseWriter, r *http.Request) {
			rr = r
			b, err := ioutil.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			msg = string(b)
		})
		app.Listen()
		Ω(waitAppRegistered(registry, app, time.Second*5)).To(BeTrue())

		url := app.Endpoint()

		buf := bytes.NewBufferString("foobar")
		r, err := http.NewRequest("PUT", url, buf)
		Ω(err).ShouldNot(HaveOccurred())

		resp, err := http.DefaultClient.Do(r)
		Ω(err).ShouldNot(HaveOccurred())
		Ω(resp.StatusCode).To(Equal(http.StatusOK))

		Ω(rr).ShouldNot(BeNil())
		Ω(rr.Method).To(Equal("PUT"))
		Ω(rr.Proto).To(Equal("HTTP/1.1"))
		Ω(msg).To(Equal("foobar"))
	})

	It("sends start on a nats connect", func() {
		started := make(chan bool)

		mbusClient.Subscribe("router.start", func(*yagnats.Message) {
			started <- true
		})

		StopNats(natsPort, natsServerCmd)
		natsServerCmd = StartNats(natsPort, true)

		Eventually(started, 1).Should(Receive())
	})

	It("supports 100 Continue", func() {
		app := test.NewTestApp([]route.Uri{"foo.vcap.me"}, Config.Port, mbusClient, nil)
		rCh := make(chan *http.Request)
		app.AddHandler("/", func(w http.ResponseWriter, r *http.Request) {
			_, err := ioutil.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
			}
			rCh <- r
		})

		app.Listen()
		go app.RegisterRepeatedly(1 * time.Second)

		Ω(waitAppRegistered(registry, app, time.Second*5)).To(BeTrue())

		host := fmt.Sprintf("foo.vcap.me:%d", Config.Port)
		conn, err := net.DialTimeout("tcp", host, 10*time.Second)
		Ω(err).ShouldNot(HaveOccurred())
		defer conn.Close()

		fmt.Fprintf(conn, "POST / HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Connection: close\r\n"+
			"Content-Length: 1\r\n"+
			"Expect: 100-continue\r\n"+
			"\r\n", host)

		fmt.Fprintf(conn, "a")

		buf := bufio.NewReader(conn)
		line, err := buf.ReadString('\n')
		Ω(err).ShouldNot(HaveOccurred())
		Ω(strings.Contains(line, "100 Continue")).To(BeTrue())

		var rr *http.Request
		Eventually(rCh).Should(Receive(&rr))
		Ω(rr).ShouldNot(BeNil())
		Ω(rr.Header.Get("Expect")).To(Equal(""))
	})

	It("handles a /routes request", func() {
		var client http.Client
		var req *http.Request
		var resp *http.Response
		var err error

		mbusClient.Publish("router.register", []byte(`{"dea":"dea1","app":"app1","uris":["test.com"],"host":"1.2.3.4","port":1234,"tags":{},"private_instance_id":"private_instance_id"}`))
		time.Sleep(250 * time.Millisecond)

		host := fmt.Sprintf("http://%s:%d/routes", Config.Ip, Config.Status.Port)

		req, err = http.NewRequest("GET", host, nil)
		req.SetBasicAuth("user", "pass")

		resp, err = client.Do(req)
		Ω(err).ShouldNot(HaveOccurred())
		Ω(resp).ShouldNot(BeNil())
		Ω(resp.StatusCode).To(Equal(200))

		body, err := ioutil.ReadAll(resp.Body)
		defer resp.Body.Close()
		Ω(err).ShouldNot(HaveOccurred())
		Ω(string(body)).Should(MatchRegexp(".*1\\.2\\.3\\.4:1234.*\n"))
	})

	It("terminates long requests", func() {
		app := test.NewSlowApp(
			[]route.Uri{"slow-app.vcap.me"},
			Config.Port,
			mbusClient,
			10*time.Second,
		)

		app.Listen()

		uri := fmt.Sprintf("http://slow-app.vcap.me:%d", Config.Port)
		resp, err := http.Get(uri)
		Ω(err).ShouldNot(HaveOccurred())
		Ω(resp.StatusCode).To(Equal(502))
	})
})

func readVarz(v vvarz.Varz) map[string]interface{} {
	varz_byte, err := v.MarshalJSON()
	Ω(err).ShouldNot(HaveOccurred())

	varz_data := make(map[string]interface{})
	err = json.Unmarshal(varz_byte, &varz_data)
	Ω(err).ShouldNot(HaveOccurred())

	return varz_data
}

func fetchRecursively(x interface{}, s ...string) interface{} {
	var ok bool

	for _, y := range s {
		z := x.(map[string]interface{})
		x, ok = z[y]
		Ω(ok).Should(BeTrue(), fmt.Sprintf("no key: %s", s))
	}

	return x
}

func verify_health_z(host string, r *rregistry.CFRegistry) {
	var req *http.Request
	var resp *http.Response
	var err error
	path := "/healthz"

	req, _ = http.NewRequest("GET", "http://"+host+path, nil)
	bytes := verify_success(req)
	Ω(string(bytes)).To(Equal("ok"))

	// Check that healthz does not reply during deadlock
	r.Lock()
	defer r.Unlock()

	httpClient := http.Client{
		Transport: &http.Transport{
			Dial: timeoutDialler(),
		},
	}

	req, err = http.NewRequest("GET", "http://"+host+path, nil)
	resp, err = httpClient.Do(req)
	Ω(err).Should(HaveOccurred())
	Ω(resp).Should(BeNil())
	Ω(err.Error()).Should(MatchRegexp("i/o timeout"))
}

func verify_var_z(host, user, pass string) {
	var client http.Client
	var req *http.Request
	var resp *http.Response
	var err error
	path := "/varz"

	// Request without username:password should be rejected
	req, _ = http.NewRequest("GET", "http://"+host+path, nil)
	resp, err = client.Do(req)
	Ω(err).ShouldNot(HaveOccurred())
	Ω(resp).ShouldNot(BeNil())
	Ω(resp.StatusCode).To(Equal(401))

	// varz Basic auth
	req.SetBasicAuth(user, pass)
	bytes := verify_success(req)
	varz := make(map[string]interface{})
	json.Unmarshal(bytes, &varz)

	Ω(varz["num_cores"]).ToNot(Equal(0))
	Ω(varz["type"]).To(Equal("Router"))
	Ω(varz["uuid"]).ToNot(Equal(""))
}

func verify_success(req *http.Request) []byte {
	var client http.Client
	resp, err := client.Do(req)
	defer resp.Body.Close()

	Ω(err).ShouldNot(HaveOccurred())
	Ω(resp).ShouldNot(BeNil())
	Ω(resp.StatusCode).To(Equal(200))

	bytes, err := ioutil.ReadAll(resp.Body)
	Ω(err).ShouldNot(HaveOccurred())

	return bytes
}

func sendRequests(url string, rPort uint16, times int) {
	uri := fmt.Sprintf("http://%s:%d", url, rPort)

	for i := 0; i < times; i++ {
		r, err := http.Get(uri)
		Ω(err).ShouldNot(HaveOccurred())

		Ω(r.StatusCode).To(Equal(http.StatusOK))
		// Close the body to avoid open files limit error
		r.Body.Close()
	}
}

func getSessionAndAppPort(url string, rPort uint16) (*http.Cookie, *http.Cookie, string) {
	var client http.Client
	var req *http.Request
	var resp *http.Response
	var err error
	var port []byte

	uri := fmt.Sprintf("http://%s:%d/sticky", url, rPort)

	req, err = http.NewRequest("GET", uri, nil)
	Ω(err).ShouldNot(HaveOccurred())

	resp, err = client.Do(req)
	Ω(err).ShouldNot(HaveOccurred())

	port, err = ioutil.ReadAll(resp.Body)
	Ω(err).ShouldNot(HaveOccurred())

	var sessionCookie, vcapCookie *http.Cookie
	for _, cookie := range resp.Cookies() {
		if cookie.Name == proxy.StickyCookieKey {
			sessionCookie = cookie
		} else if cookie.Name == proxy.VcapCookieId {
			vcapCookie = cookie
		}
	}

	return sessionCookie, vcapCookie, string(port)
}

func getAppPortWithSticky(url string, rPort uint16, sessionCookie, vcapCookie *http.Cookie) string {
	var client http.Client
	var req *http.Request
	var resp *http.Response
	var err error
	var port []byte

	uri := fmt.Sprintf("http://%s:%d/sticky", url, rPort)

	req, err = http.NewRequest("GET", uri, nil)
	Ω(err).ShouldNot(HaveOccurred())

	req.AddCookie(sessionCookie)
	req.AddCookie(vcapCookie)

	resp, err = client.Do(req)
	Ω(err).ShouldNot(HaveOccurred())

	port, err = ioutil.ReadAll(resp.Body)

	return string(port)
}
