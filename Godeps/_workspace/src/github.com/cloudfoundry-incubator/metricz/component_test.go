package metricz

import (
	"encoding/json"
	"io/ioutil"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/cloudfoundry-incubator/metricz/instrumentation"
	"github.com/cloudfoundry/loggregatorlib/loggertesthelper"
	"github.com/stretchr/testify/assert"
)

type GoodHealthMonitor struct{}

func (hm GoodHealthMonitor) Ok() bool {
	return true
}

type BadHealthMonitor struct{}

func (hm BadHealthMonitor) Ok() bool {
	return false
}

func TestComponentURL(t *testing.T) {
	component, err := NewComponent(loggertesthelper.Logger(), "loggregator", 0, GoodHealthMonitor{}, 0, nil, nil)
	assert.NoError(t, err)

	url := component.URL()

	host, port, err := net.SplitHostPort(url.Host)
	assert.NoError(t, err)

	assert.Equal(t, url.Scheme, "http")

	assert.NotEqual(t, host, "0.0.0.0")
	assert.NotEqual(t, host, "127.0.0.1")

	assert.NotEqual(t, port, "0")
}

func TestStatusCredentialsNil(t *testing.T) {
	component, err := NewComponent(loggertesthelper.Logger(), "loggregator", 0, GoodHealthMonitor{}, 0, nil, nil)
	assert.NoError(t, err)

	url := component.URL()

	assert.NotEmpty(t, url.User.Username())

	_, passwordPresent := url.User.Password()
	assert.True(t, passwordPresent)
}

func TestStatusCredentialsDefault(t *testing.T) {
	component, err := NewComponent(loggertesthelper.Logger(), "loggregator", 0, GoodHealthMonitor{}, 0, []string{"", ""}, nil)
	assert.NoError(t, err)

	url := component.URL()

	assert.NotEmpty(t, url.User.Username())

	_, passwordPresent := url.User.Password()
	assert.True(t, passwordPresent)
}

func TestGoodHealthzEndpoint(t *testing.T) {
	component, err := NewComponent(
		loggertesthelper.Logger(),
		"loggregator",
		0,
		GoodHealthMonitor{},
		7877,
		[]string{"user", "pass"},
		[]instrumentation.Instrumentable{},
	)
	assert.NoError(t, err)

	go component.StartMonitoringEndpoints()

	req, err := http.NewRequest("GET", component.URL().String()+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)

	assert.Equal(t, resp.StatusCode, 200)
	assert.Equal(t, resp.Header.Get("Content-Type"), "text/plain")
	body, err := ioutil.ReadAll(resp.Body)
	assert.NoError(t, err)
	assert.Equal(t, string(body), "ok")
}

func TestBadHealthzEndpoint(t *testing.T) {
	component, err := NewComponent(
		loggertesthelper.Logger(),
		"loggregator",
		0,
		BadHealthMonitor{},
		9878,
		[]string{"user", "pass"},
		[]instrumentation.Instrumentable{},
	)
	assert.NoError(t, err)

	go component.StartMonitoringEndpoints()

	req, err := http.NewRequest("GET", component.URL().String()+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)

	assert.Equal(t, resp.StatusCode, 200)
	body, err := ioutil.ReadAll(resp.Body)
	assert.NoError(t, err)
	assert.Equal(t, string(body), "bad")
}

func TestPanicWhenFailingToMonitorEndpoints(t *testing.T) {
	component, err := NewComponent(
		loggertesthelper.Logger(),
		"loggregator",
		0,
		GoodHealthMonitor{},
		7879,
		[]string{"user", "pass"},
		[]instrumentation.Instrumentable{},
	)
	assert.NoError(t, err)

	finishChan := make(chan bool)

	go func() {
		err := component.StartMonitoringEndpoints()
		assert.NoError(t, err)
	}()
	time.Sleep(50 * time.Millisecond)

	go func() {
		//Monitoring a second time should fail because the port is already in use.
		err := component.StartMonitoringEndpoints()
		assert.Error(t, err)
		finishChan <- true
	}()

	<-finishChan
}

func TestStoppingServer(t *testing.T) {
	component, err := NewComponent(
		loggertesthelper.Logger(),
		"loggregator",
		0,
		GoodHealthMonitor{},
		7885,
		[]string{"user", "pass"},
		[]instrumentation.Instrumentable{},
	)
	assert.NoError(t, err)

	go func() {
		err := component.StartMonitoringEndpoints()
		assert.NoError(t, err)
	}()

	time.Sleep(50 * time.Millisecond)

	component.StopMonitoringEndpoints()

	go func() {
		err := component.StartMonitoringEndpoints()
		assert.NoError(t, err)
	}()
}

type testInstrumentable struct {
	name    string
	metrics []instrumentation.Metric
}

func (t testInstrumentable) Emit() instrumentation.Context {
	return instrumentation.Context{Name: t.name, Metrics: t.metrics}
}

func TestVarzRequiresBasicAuth(t *testing.T) {
	tags := map[string]interface{}{"tagName1": "tagValue1", "tagName2": "tagValue2"}
	component, err := NewComponent(
		loggertesthelper.Logger(),
		"loggregator",
		0,
		GoodHealthMonitor{},
		1234,
		[]string{"user", "pass"},
		[]instrumentation.Instrumentable{
			testInstrumentable{
				"agentListener",
				[]instrumentation.Metric{
					instrumentation.Metric{Name: "messagesReceived", Value: 2004},
					instrumentation.Metric{Name: "queueLength", Value: 5, Tags: tags},
				},
			},
			testInstrumentable{
				"cfSinkServer",
				[]instrumentation.Metric{
					instrumentation.Metric{Name: "activeSinkCount", Value: 3},
				},
			},
		},
	)
	assert.NoError(t, err)

	go component.StartMonitoringEndpoints()

	unauthenticatedURL := component.URL()
	unauthenticatedURL.User = nil
	unauthenticatedURL.Path = "/varz"

	req, err := http.NewRequest("GET", unauthenticatedURL.String(), nil)
	assert.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, resp.StatusCode, 401)
}

func TestVarzEndpoint(t *testing.T) {
	tags := map[string]interface{}{"tagName1": "tagValue1", "tagName2": "tagValue2"}
	component, err := NewComponent(
		loggertesthelper.Logger(),
		"loggregator",
		0,
		GoodHealthMonitor{},
		1234,
		[]string{"user", "pass"},
		[]instrumentation.Instrumentable{
			testInstrumentable{
				"agentListener",
				[]instrumentation.Metric{
					instrumentation.Metric{Name: "messagesReceived", Value: 2004},
					instrumentation.Metric{Name: "queueLength", Value: 5, Tags: tags},
				},
			},
			testInstrumentable{
				"cfSinkServer",
				[]instrumentation.Metric{
					instrumentation.Metric{Name: "activeSinkCount", Value: 3},
				},
			},
		},
	)
	assert.NoError(t, err)

	go component.StartMonitoringEndpoints()

	req, err := http.NewRequest("GET", component.URL().String()+"/varz", nil)
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)

	memStats := new(runtime.MemStats)
	runtime.ReadMemStats(memStats)

	assert.Equal(t, resp.StatusCode, 200)
	assert.Equal(t, resp.Header.Get("Content-Type"), "application/json")
	body, err := ioutil.ReadAll(resp.Body)
	assert.NoError(t, err)

	expected := map[string]interface{}{
		"name":          "loggregator",
		"numCPUS":       runtime.NumCPU(),
		"numGoRoutines": runtime.NumGoroutine(),
		"memoryStats": map[string]interface{}{
			"numBytesAllocatedHeap":  int(memStats.HeapAlloc),
			"numBytesAllocatedStack": int(memStats.StackInuse),
			"numBytesAllocated":      int(memStats.Alloc),
			"numMallocs":             int(memStats.Mallocs),
			"numFrees":               int(memStats.Frees),
			"lastGCPauseTimeNS":      int(memStats.PauseNs[(memStats.NumGC+255)%256]),
		},
		"tags": map[string]string{
			"ip": "something",
		},
		"contexts": []interface{}{
			map[string]interface{}{
				"name": "agentListener",
				"metrics": []interface{}{
					map[string]interface{}{
						"name":  "messagesReceived",
						"value": 2004,
					},
					map[string]interface{}{
						"name":  "queueLength",
						"value": 5,
						"tags": map[string]interface{}{
							"tagName1": "tagValue1",
							"tagName2": "tagValue2",
						},
					},
				},
			},
			map[string]interface{}{
				"name": "cfSinkServer",
				"metrics": []interface{}{
					map[string]interface{}{
						"name":  "activeSinkCount",
						"value": 3,
					},
				},
			},
		},
	}

	var actualMap map[string]interface{}
	json.Unmarshal(body, &actualMap)
	assert.NotNil(t, actualMap["tags"])
	assert.Equal(t, expected["contexts"], actualMap["contexts"])
	assert.Equal(t, expected["name"], actualMap["name"])
	assert.Equal(t, expected["numCPUS"], actualMap["numCPUS"])
	assert.Equal(t, expected["numGoRoutines"], actualMap["numGoRoutines"])
	assert.NotNil(t, actualMap["memoryStats"])
	assert.NotEmpty(t, actualMap["memoryStats"])
}
