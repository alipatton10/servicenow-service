package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/alipatton10/servicenow-service/utils"
	"github.com/cloudevents/sdk-go/pkg/cloudevents"
	"github.com/cloudevents/sdk-go/pkg/cloudevents/client"
	cloudeventshttp "github.com/cloudevents/sdk-go/pkg/cloudevents/transport/http"
	"github.com/kelseyhightower/envconfig"

	keptnevents "github.com/keptn/go-utils/pkg/events"
	keptnutils "github.com/keptn/go-utils/pkg/utils"
)

// Event is the object required for wrapping event data...
type Event struct {
	Records []Records `json:"records"`
}

// Records is the fields contained within the event ...
type Records struct {
	Source          string `json:"source"`
	EventClass      string `json:"event_class"`
	Resource        string `json:"resource"`
	Node            string `json:"node"`
	MetricName      string `json:"metric_name"`
	Type            string `json:"type"`
	MessageKey      string `json:"message_key"`
	Severity        string `json:"severity"`
	Description     string `json:"description"`
	ResolutionState string `json:"resolution_state"`
	AdditionalInfo  string `json:"additional_info"`
}

type problemDetailsData struct {
	DisplayName   string `json:"displayName"`
	ImpactLevel   string `json:"impactLevel"`
	SeverityLevel string `json:"severityLevel"`
	Status        string `json:"status"`
}

const eventbroker = "EVENTBROKER"

type envConfig struct {
	// Port on which to listen for cloudevents
	Port int    `envconfig:"RCV_PORT" default:"8080"`
	Path string `envconfig:"RCV_PATH" default:"/"`
}

func main() {
	var env envConfig
	if err := envconfig.Process("", &env); err != nil {
		log.Fatalf("Failed to process env var: %s", err)
	}
	os.Exit(_main(os.Args[1:], env))
}

func gotEvent(ctx context.Context, event cloudevents.Event) error {
	var shkeptncontext string
	event.Context.ExtensionAs("shkeptncontext", &shkeptncontext)

	logger := keptnutils.NewLogger(shkeptncontext, event.Context.GetID(), "servicenow-service")

	data := &keptnevents.ProblemEventData{}
	if err := event.DataAs(data); err != nil {
		logger.Error(fmt.Sprintf("Got Data Error: %s", err.Error()))
		return err
	}

	if event.Type() != keptnevents.ProblemEventType {
		const errorMsg = "Received unexpected keptn event"
		logger.Error(errorMsg)
		logger.Error(event.Type())
		return errors.New(errorMsg)
	}

	go createEvent(event, shkeptncontext, *data, logger)

	return nil
}

func createEvent(event cloudevents.Event, shkeptncontext string, data keptnevents.ProblemEventData, logger *keptnutils.Logger) error {

	kubeClient, err := keptnutils.GetKubeAPI(true)

	if err != nil {
		logger.Error("could not create Kubernetes client")
		return errors.New("could not create Kubernetes client")
	}

	snowAPIURL, snowUser, snowPassword, err := utils.GetGlobalServicenowCredentials(kubeClient, logger)

	if err != nil {
		logger.Debug(err.Error())
		logger.Debug("Failed to fetch global ServiceNow credentials, exiting.")
		return err
	}

	var EventSource string = "KEPTN"
	var KeptnImpactedEntity = strings.Split(data.ImpactedEntity, " ")
	var KeptnNode string = KeptnImpactedEntity[len(KeptnImpactedEntity)-1]

	var problemInfo problemDetailsData
	ProblemData := []byte(data.ProblemDetails)
	Source := (*json.RawMessage)(&ProblemData)
	err = json.Unmarshal(*Source, &problemInfo)
	if err != nil {
		panic(err)
	}

	// Map eventtype to ServiceNow event severity
	var severity string = ""
	// Map problem status to ServiceNow event resolution state
	var resolutionState = ""

	switch problemInfo.SeverityLevel {
	case "AVAILABILITY":
		severity = "1"
	case "ERROR":
		severity = "2"
	case "PERFORMANCE":
		severity = "3"
	case "RESOURCE_CONTENTION":
		severity = "4"
	case "CUSTOM_ALERT":
		severity = "3"
	default:
		severity = "4"
	}

	if problemInfo.Status == "CLOSED" {
		resolutionState = "Closing"
	} else {
		resolutionState = "New"
	}

	SnowEvent := Event{
		Records: []Records{
			Records{
				Source:          EventSource,
				EventClass:      event.Source(),
				Node:            KeptnNode,
				MetricName:      data.ProblemTitle,
				Type:            data.ProblemTitle,
				MessageKey:      data.PID,
				Severity:        severity,
				Description:     data.ProblemTitle,
				ResolutionState: resolutionState,
				AdditionalInfo:  "{'KeptnSource':'" + event.Source() + "', 'KeptnContext':'" + shkeptncontext + "', 'KeptnImpactedEntity':'" + KeptnNode + "', 'KeptnPID':'" + data.PID + "', 'KeptnProblemTitle':'" + data.ProblemTitle + "', 'KeptnProblemState':'" + data.State + "', 'KeptnProject':'" + data.Project + "', 'KeptnStage':'" + data.Stage + "', 'KeptnService':'" + data.Service + "', 'KeptnProblemID':'" + data.ProblemID + "', 'KeptnImpactLevel':'" + problemInfo.ImpactLevel + "', 'KeptnSeverityLevel':'" + problemInfo.SeverityLevel + "'}",
			},
		},
	}

	var EventData []byte
	EventData, err = json.Marshal(SnowEvent)
	if err != nil {
		logger.Debug(err.Error())
	}

	var jsonString = string(EventData)
	var jsonData = strings.NewReader(jsonString)

	client := &http.Client{Timeout: time.Second * 10}
	req, err := http.NewRequest("POST", snowAPIURL, jsonData)
	req.Header.Add("accept", "application/json")
	req.Header.Add("Content-Type", "application/json")
	req.SetBasicAuth(snowUser, snowPassword)

	if err != nil {
		logger.Debug(err.Error())
	}

	res, err := client.Do(req)
	if err != nil {
		logger.Debug(err.Error())
	}

	defer res.Body.Close()
	body, err := ioutil.ReadAll(res.Body)

	fmt.Println(string(body))

	return nil

}

func _main(args []string, env envConfig) int {

	ctx := context.Background()

	t, err := cloudeventshttp.New(
		cloudeventshttp.WithPort(env.Port),
		cloudeventshttp.WithPath(env.Path),
	)

	if err != nil {
		log.Fatalf("failed to create transport, %v", err)
	}
	c, err := client.New(t)
	if err != nil {
		log.Fatalf("failed to create client, %v", err)
	}

	log.Fatalf("failed to start receiver: %s", c.StartReceiver(ctx, gotEvent))

	return 0
}

func sendEvent(event cloudevents.Event) error {
	endPoint, err := getServiceEndpoint(eventbroker)
	if err != nil {
		return errors.New("Failed to retrieve endpoint of eventbroker. %s" + err.Error())
	}

	if endPoint.Host == "" {
		return errors.New("Host of eventbroker not set")
	}

	transport, err := cloudeventshttp.New(
		cloudeventshttp.WithTarget(endPoint.String()),
		cloudeventshttp.WithEncoding(cloudeventshttp.StructuredV02),
	)
	if err != nil {
		return errors.New("Failed to create transport:" + err.Error())
	}

	c, err := client.New(transport)
	if err != nil {
		return errors.New("Failed to create HTTP client:" + err.Error())
	}

	if _, err := c.Send(context.Background(), event); err != nil {
		return errors.New("Failed to send cloudevent:, " + err.Error())
	}
	return nil
}

// getServiceEndpoint gets an endpoint stored in an environment variable and sets http as default scheme
func getServiceEndpoint(service string) (url.URL, error) {
	url, err := url.Parse(os.Getenv(service))
	if err != nil {
		return *url, fmt.Errorf("Failed to retrieve value from ENVIRONMENT_VARIABLE: %s", service)
	}

	if url.Scheme == "" {
		url.Scheme = "http"
	}

	return *url, nil
}
