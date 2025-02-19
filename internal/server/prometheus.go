package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"golang.org/x/exp/maps"

	"github.com/prometheus/common/model"
	"go.uber.org/zap"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/config"
)

// ThresholdResponse represents the response format for a threshold.
type ThresholdResponse struct {
	Data  json.RawMessage `json:"data"`
	Key   string          `json:"key"`
	Name  string          `json:"name"`
	Color string          `json:"color"`
	Value string          `json:"value"`
	Unit  string          `json:"unit"`
}

// AggregatedResponse represents the final output response structure returned by execute function
type AggregatedResponse struct {
	Data       json.RawMessage     `json:"data"`
	Thresholds []ThresholdResponse `json:"thresholds,omitempty"`
}

type PrometheusProvider struct {
	logger     *zap.SugaredLogger
	config     *MetricsEndpointConfig
	apiClients map[string]v1.API
}

func (pp *PrometheusProvider) getType() string {
	return PROMETHEUS_TYPE
}

// getDashboard returns the dashboard configuration for the specified application
func (pp *PrometheusProvider) getDashboard(ctx *gin.Context) {
	appName := ctx.Param("application")
	groupKind := ctx.Param("groupkind")
	app := pp.config.getApp(appName)
	if app == nil {
		ctx.JSON(http.StatusBadRequest, "Requested/Default Application not found")
		return
	}
	dash := app.getDashBoard(groupKind)

	if dash == nil {
		ctx.JSON(http.StatusBadRequest, "Requested/Default Dashboard not found")
		return
	}
	dash.ProviderType = pp.getType()
	ctx.JSON(http.StatusOK, dash)
}

func NewPrometheusProvider(prometheusConfig *MetricsEndpointConfig, logger *zap.SugaredLogger) (*PrometheusProvider, error) {
	if prometheusConfig != nil {
		switch {
		case len(prometheusConfig.Endpoints) == 0:
			return nil, errors.New("no endpoints specified in prometheus config")
		case len(prometheusConfig.Endpoints) == 1:
			// if 'defaultEndpoint' is unspecified in config, and there's only one endpoint,
			// then set it as default.
			if len(prometheusConfig.DefaultEndpoint) == 0 {
				prometheusConfig.DefaultEndpoint = maps.Keys(prometheusConfig.Endpoints)[0]
			}
		case len(prometheusConfig.Endpoints) > 1 && len(prometheusConfig.DefaultEndpoint) == 0:
			return nil, errors.New("'defaultEndpoint' must be specified when multiple prometheus endpoints are defined")
		}

		prometheusProvider := &PrometheusProvider{config: prometheusConfig, apiClients: map[string]v1.API{}, logger: logger}

		if err := prometheusProvider.init(); err != nil {
			return nil, err
		}

		return prometheusProvider, nil
	}
	return nil, errors.New("prometheus provider config section is defined, but empty")
}

func (ep *metricsEndpoint) getBearerToken() config.SecretReader {
	if len(ep.BearerToken) > 0 {
		return config.NewInlineSecret(ep.BearerToken)
	} else if len(ep.BearerTokenFile) > 0 {
		return config.NewFileSecret(ep.BearerTokenFile)
	}

	return nil
}

func (pp *PrometheusProvider) getClient(ctx *gin.Context) (v1.API, error) {
	if len(maps.Keys(pp.apiClients)) == 1 {
		// Only one client, so return the "first"
		return maps.Values(pp.apiClients)[0], nil
	}

	// Argocd-Application-Name header value follows a pattern of "<namespace>:<app-name>"
	// We just want the <app-name> part
	// https://argo-cd.readthedocs.io/en/stable/developer-guide/extensions/proxy-extensions/#argocd-application-name-mandatory
	applicationNameHeader := strings.Split(ctx.Request.Header["Argocd-Application-Name"][0], ":")[1]
	projectNameHeader := ctx.Request.Header["Argocd-Project-Name"][0]

	name, err := pp.config.getEndpointMatchIfExists(applicationNameHeader, projectNameHeader)
	if err != nil {
		return nil, err
	}
	client, hasKey := pp.apiClients[name]
	if hasKey {
		return client, nil
	}

	// Requested client doesn't exist, so return default client
	client, hasKey = pp.apiClients[pp.config.DefaultEndpoint]
	if hasKey {
		return client, nil
	} else {
		// Default client also doesn't exist, so throw an error
		return nil, errors.New("no matching client was found, and the default client is either unspecified or incorrect")
	}
}

func (pp *PrometheusProvider) init() error {
	for name, endpoint := range pp.config.Endpoints {
		var rt http.RoundTripper
		if token := endpoint.getBearerToken(); token != nil {
			rt = config.NewAuthorizationCredentialsRoundTripper(
				"Bearer",
				token,
				api.DefaultRoundTripper,
			)
		} else {
			rt = api.DefaultRoundTripper
		}

		client, err := api.NewClient(api.Config{
			Address:      endpoint.URL,
			RoundTripper: rt,
		})
		if err != nil {
			pp.logger.Errorf("an error occurred while initializing endpoint %s: error creating client: %v\n", name, err)
			return err
		}

		pp.apiClients[name] = v1.NewAPI(client)
	}

	return nil
}

// executeGraphQuery executes a prometheus query and returns the result.
func executeGraphQuery(ctx *gin.Context, queryExpression string, env map[string][]string, duration time.Duration, pp *PrometheusProvider) (model.Value, v1.Warnings, error) {
	tmpl, err := template.New("query").Parse(queryExpression)
	if err != nil {
		return nil, nil, fmt.Errorf("error parsing query template: %s", err)
	}

	env1 := make(map[string]string)
	for k, v := range env {
		env1[k] = strings.Join(v, ",")
	}

	buf := new(bytes.Buffer)
	err = tmpl.Execute(buf, env1)
	if err != nil {
		return nil, nil, fmt.Errorf("error executing template: %s", err)
	}

	strQuery := buf.String()
	r := v1.Range{
		Start: time.Now().Add(-duration),
		End:   time.Now(),
		Step:  time.Minute,
	}
	client, err := pp.getClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("error fetching client: %s", err)
	}

	result, warnings, err := client.QueryRange(ctx, strQuery, r)

	if err != nil {
		return nil, warnings, fmt.Errorf("error querying prometheus: %s", err)
	}

	if len(warnings) > 0 {
		return result, warnings, fmt.Errorf("query warnings: %s", err)
	}

	return result, nil, nil
}

// execute handles the execution of a graph queryExpression and graph thresholds
func (pp *PrometheusProvider) execute(ctx *gin.Context) {
	app := ctx.Param("application")
	groupKind := ctx.Param("groupkind")
	rowName := ctx.Param("row")
	graphName := ctx.Param("graph")
	durationStr := ctx.Query("duration")
	if durationStr == "" {
		durationStr = "1h"
	}
	duration, err := time.ParseDuration(durationStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, "Invalid duration format :"+err.Error())
		return
	}

	env := ctx.Request.URL.Query()

	application := pp.config.getApp(app)
	if application == nil {
		ctx.JSON(http.StatusBadRequest, "Requested/Default Application not found")
		return
	}
	dashboard := application.getDashBoard(groupKind)
	if dashboard == nil {
		ctx.JSON(http.StatusBadRequest, "Requested/Default Dashboard not found")
		return
	}
	row := dashboard.getRow(rowName)
	if row == nil {
		ctx.JSON(http.StatusBadRequest, "Requested Row not found")
		return
	}
	graph := row.getGraph(graphName)
	if graph != nil {

		var data AggregatedResponse
		result, warnings, err := executeGraphQuery(ctx, graph.QueryExpression, env, duration, pp)

		if err != nil {
			ctx.JSON(http.StatusBadRequest, err)
			return
		}
		if len(warnings) > 0 {
			warningMsg := fmt.Errorf("query warnings: %s", warnings)
			ctx.JSON(http.StatusBadRequest, warningMsg.Error())
			return
		}
		data.Data, err = json.Marshal(result)
		if err != nil {
			ctx.JSON(http.StatusBadRequest, fmt.Errorf("error marshaling the data: %s", err))
			return
		}
		var finalResultArr []ThresholdResponse
		if graph.Thresholds != nil {

			for _, threshold := range graph.Thresholds {
				var result model.Value
				var warnings v1.Warnings
				var err error

				//If threshold.value present, threshold.value gets executed else,threshold.queryExpression gets executed.
				if threshold.Value != "" {
					result, warnings, err = executeGraphQuery(ctx, threshold.Value, env, duration, pp)
				} else {
					result, warnings, err = executeGraphQuery(ctx, threshold.QueryExpression, env, duration, pp)
				}
				if err != nil {
					ctx.JSON(http.StatusBadRequest, err)
					return
				}
				if len(warnings) > 0 {
					warningMsg := fmt.Errorf("query warnings: %s", warnings)
					ctx.JSON(http.StatusBadRequest, warningMsg.Error())
					return
				}
				var temp ThresholdResponse
				temp.Unit = threshold.Unit
				temp.Name = threshold.Name
				temp.Value = threshold.Value
				temp.Key = threshold.Key
				temp.Color = threshold.Color
				temp.Data, err = json.Marshal(result)
				if err != nil {
					ctx.JSON(http.StatusBadRequest, fmt.Errorf("error marshaling the threshold response: %s", err))
					return
				}

				finalResultArr = append(finalResultArr, temp)
			}
		}
		data.Thresholds = finalResultArr

		ctx.JSON(http.StatusOK, data)
		return
	}
}
