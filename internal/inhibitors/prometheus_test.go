package inhibitors

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	papi "github.com/prometheus/client_golang/api"
	"github.com/stretchr/testify/assert"
)

type MockResponse struct {
	StatusCode int
	Body       []byte
}

// MockServerProperties ties a mock response to a url and a method
type MockServerProperties struct {
	URI        string
	HTTPMethod string
	Response   MockResponse
}

// NewMockServer sets up a new MockServer with properties ad starts the server.
func NewMockServer(props ...MockServerProperties) *httptest.Server {

	handler := http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			for _, proc := range props {
				_, err := w.Write(proc.Response.Body)
				if err != nil {
					log.Fatal(err)
				}
			}
		})
	return httptest.NewServer(handler)
}

func TestActiveAlerts(t *testing.T) {
	responsebody := `{"status":"success","data":{"resultType":"vector","result":[
		{"metric":{"__name__":"ALERTS","alertname":"GatekeeperViolations","alertstate":"firing","severity":"warning","team":"platform-infra"},"value":[1622472933.973,"1"]},
		{"metric":{"__name__":"ALERTS","alertname":"PodCrashing-dev","alertstate":"firing","container":"deployment","instance":"1.2.3.4:8080","job":"kube-state-metrics","namespace":"dev","pod":"dev-deployment-78dcbmf25v","severity":"critical","team":"dev"},"value":[1622472933.973,"1"]},
		{"metric":{"__name__":"ALERTS","alertname":"PodRestart-dev","alertstate":"firing","container":"deployment","instance":"1.2.3.4:1234","job":"kube-state-metrics","namespace":"qa","pod":"qa-job-deployment-78dcbmf25v","severity":"warning","team":"qa"},"value":[1622472933.973,"1"]},
		{"metric":{"__name__":"ALERTS","alertname":"PodRestart-another","alertstate":"firing","container":"deployment","instance":"1.2.3.4:1234","job":"kube-state-metrics","namespace":"qa","severity":"warning","team":"qa"},"value":[1622472933.973,"1"]},
		{"metric":{"__name__":"ALERTS","alertname":"PodRestart-another2","alertstate":"pending","container":"deployment","instance":"1.2.3.4:1234","job":"kube-state-metrics","namespace":"qa","severity":"warning","team":"qa"},"value":[1622472933.973,"1"]},
		{"metric":{"__name__":"ALERTS","alertname":"PrometheusTargetDown","alertstate":"firing","job":"kubernetes-pods","severity":"warning","team":"platform-infra"},"value":[1622472933.973,"1"]},
		{"metric":{"__name__":"ALERTS","alertname":"ScheduledRebootFailing","alertstate":"pending","severity":"warning","team":"platform-infra"},"value":[1622472933.973,"1"]}
	]}}`
	invalidResponseBody := `{"status":"error","data":{"resultType":"vector","result":[]}}`
	addr := "http://localhost:10001"

	for _, tc := range []struct {
		it              string
		rFilter         string
		respBody        string
		aName           string
		wantN           int
		firingOnly      bool
		filterMatchOnly bool
	}{
		{
			it:              "should return no alerts",
			respBody:        responsebody,
			rFilter:         "",
			wantN:           0,
			firingOnly:      false,
			filterMatchOnly: false,
		},
		{
			it:              "filter everything out",
			respBody:        responsebody,
			rFilter:         ".*",
			wantN:           0,
			firingOnly:      false,
			filterMatchOnly: false,
		},
		{
			it:              "filter everything out regardless of firing or not",
			respBody:        responsebody,
			rFilter:         ".*",
			wantN:           0,
			firingOnly:      true,
			filterMatchOnly: false,
		},
		{
			it:              "must keep everything",
			respBody:        responsebody,
			rFilter:         ".*",
			wantN:           7,
			firingOnly:      false,
			filterMatchOnly: true,
		},
		{
			it:              "must keep only firing",
			respBody:        responsebody,
			rFilter:         ".*",
			wantN:           5,
			firingOnly:      true,
			filterMatchOnly: true,
		},
		{
			it:              "everything except matching alertnames",
			respBody:        responsebody,
			rFilter:         "Pod",
			wantN:           3,
			firingOnly:      false,
			filterMatchOnly: false,
		},
		{
			it:              "all firing except matching alertnames",
			respBody:        responsebody,
			rFilter:         "Pod",
			wantN:           2,
			firingOnly:      true,
			filterMatchOnly: false,
		},
		{
			it:              "only matching alertnames regardless of firing or not",
			respBody:        responsebody,
			rFilter:         "Pod",
			wantN:           4,
			firingOnly:      false,
			filterMatchOnly: true,
		},
		{
			it:              "only matching alertnames actively firing",
			respBody:        responsebody,
			rFilter:         "Pod",
			wantN:           3,
			firingOnly:      true,
			filterMatchOnly: true,
		},
		{
			it:              "invalid response",
			respBody:        invalidResponseBody,
			rFilter:         ".*",
			wantN:           0,
			firingOnly:      false,
			filterMatchOnly: false,
		},
		{
			it:              "invalid response 2",
			respBody:        invalidResponseBody,
			rFilter:         ".*",
			wantN:           0,
			firingOnly:      false,
			filterMatchOnly: true,
		},
	} {
		// Start mockServer
		mockServer := NewMockServer(MockServerProperties{
			URI:        addr,
			HTTPMethod: http.MethodPost,
			Response: MockResponse{
				Body: []byte(tc.respBody),
			},
		})
		// Close mockServer after all connections are gone
		defer mockServer.Close()

		client, _ := papi.NewClient(papi.Config{Address: mockServer.URL})

		t.Run(tc.it, func(t *testing.T) {

			// regex filter
			regex, _ := regexp.Compile(tc.rFilter)

			// instantiate the prometheus client with the mockserver-address
			result, _ := findAlerts(context.Background(), client, regex, tc.firingOnly, tc.filterMatchOnly)
			//fmt.Println(result)

			// assert
			assert.Equal(t, tc.wantN, len(result), "expected amount of alerts %v, got %v", tc.wantN, len(result))

			if tc.aName != "" {
				assert.Equal(t, tc.aName, result[0], "expected active alert %v, got %v", tc.aName, result[0])
			}
		})
	}
}
