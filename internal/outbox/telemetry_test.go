package outbox

import (
	"context"
	"net/url"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/stagdhq/outbox/internal/telemetry"
)

// Each request's span is named for what the app asked, and the metrics count
// the emails by what became of them and the texts, by vendor.
func TestTelemetry(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	reader := sdkmetric.NewManualReader()

	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	addr, _ := smtpServer(t)

	s := &Server{SMTP: addr}
	vendors := telemetry.Handler(s.Handler())
	aws := telemetry.Handler(s.AWSHandler())

	post(t, vendors, "/resend/emails", "application/json", `{"from": "a@example.com", "to": "b@example.com", "subject": "Hi", "text": "Hello"}`)
	post(t, aws, "/", "application/x-www-form-urlencoded", url.Values{"Action": {"Publish"}, "PhoneNumber": {"+4791234567"}, "Message": {"Your code is 123456"}}.Encode())

	var names []string
	for _, span := range spans.Ended() {
		names = append(names, span.Name())
	}

	if len(names) != 2 || names[0] != "POST /resend/emails" || names[1] != "AWS Publish" {
		t.Fatalf("spans: %v", names)
	}

	var rm metricdata.ResourceMetrics

	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}

	count := func(name string, attrs ...attribute.KeyValue) int64 {
		var total int64

		for _, scope := range rm.ScopeMetrics {
			for _, m := range scope.Metrics {
				sum, ok := m.Data.(metricdata.Sum[int64])
				if m.Name != name || !ok {
					continue
				}

				for _, point := range sum.DataPoints {
					matches := true

					for _, want := range attrs {
						if got, ok := point.Attributes.Value(want.Key); !ok || got != want.Value {
							matches = false
						}
					}

					if matches {
						total += point.Value
					}
				}
			}
		}

		return total
	}

	if got := count("outbox.emails", attribute.String("outbox.vendor", "resend"), attribute.String("outbox.outcome", "passed")); got != 1 {
		t.Errorf("emails passed on: %d", got)
	}

	if got := count("outbox.texts", attribute.String("outbox.vendor", "sns")); got != 1 {
		t.Errorf("texts: %d", got)
	}
}
