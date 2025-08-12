package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"github.com/spf13/viper"
)

var promClient v1.API

// prometheusArgs defines the arguments for Prometheus queries
type prometheusArgs struct {
	Query string `json:"query"`           // PromQL query string
	Start string `json:"start,omitempty"` // Start time in RFC3339 format
	End   string `json:"end,omitempty"`   // End time in RFC3339 format
	Step  string `json:"step,omitempty"`  // Step duration (e.g. "15s", "1m", "1h")
}

func initPrometheus() error {
	baseURL := fmt.Sprintf("http://%s:%d",
		viper.GetString("prometheus.host"),
		viper.GetInt("prometheus.port"),
	)

	// Handle VictoriaMetrics cluster mode
	if viper.GetBool("prometheus.vm_cluster_mode") {
		tenantID := viper.GetString("prometheus.vm_tenant_id")
		pathPrefix := viper.GetString("prometheus.vm_path_prefix")
		if pathPrefix == "" {
			pathPrefix = "prometheus"
		}
		baseURL = fmt.Sprintf("%s/select/%s/%s", baseURL, tenantID, pathPrefix)
	}

	cfg := api.Config{
		Address: baseURL,
	}

	client, err := api.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("error creating prometheus client: %v", err)
	}

	promClient = v1.NewAPI(client)
	return nil
}

// queryPrometheus executes a PromQL query and returns the results
func queryPrometheus(query string, start, end time.Time, step time.Duration) (interface{}, error) {
	if promClient == nil {
		return nil, fmt.Errorf("prometheus client not initialized")
	}

	ctx := context.Background()
	r := v1.Range{
		Start: start,
		End:   end,
		Step:  step,
	}

	result, _, err := promClient.QueryRange(ctx, query, r)
	if err != nil {
		return nil, fmt.Errorf("error querying prometheus: %v", err)
	}

	return summarizePromResult(result)
}

func validateAndParseTimeRange(start, end string) (time.Time, time.Time, error) {
	parsed_start, err := parseTime(start)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start time format: %v", err)
	}

	parsed_end := time.Now()
	if end != "" {
		parsed_end, err = parseTime(end)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end time format: %v", err)
		}
	}

	if parsed_start.After(parsed_end) {
		return time.Time{}, time.Time{}, fmt.Errorf("start time must be before end time")
	}

	return parsed_start, parsed_end, nil
}

func parseTime(timeStr string) (time.Time, error) {
	if strings.HasPrefix(timeStr, "-") {
		d, err := time.ParseDuration(timeStr)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid relative time: %v", err)
		}
		return time.Now().Add(d), nil
	}
	return time.Parse(time.RFC3339, timeStr)
}

// summarizePromResult processes the raw Prometheus result
func summarizePromResult(result interface{}) (interface{}, error) {
	matrix, ok := result.(model.Matrix)
	if !ok {
		// For non-matrix results (scalar, vector), return as is
		return result, nil
	}

	if len(matrix) == 0 {
		return result, nil
	}

	// For matrix results, just get the last value from each series
	var lastValues []map[string]interface{}
	for _, series := range matrix {
		if len(series.Values) > 0 {
			lastPoint := series.Values[len(series.Values)-1]
			lastValues = append(lastValues, map[string]interface{}{
				"metric": series.Metric,
				"value":  lastPoint.Value,
				"time":   lastPoint.Timestamp.Time(),
			})
		}
	}

	return map[string]interface{}{
		"raw_result":  result,
		"last_values": lastValues,
	}, nil
}
