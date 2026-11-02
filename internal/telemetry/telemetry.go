// Package telemetry is outbox's OpenTelemetry: traces and metrics over
// OTLP/HTTP, on only when an OTLP endpoint is set (OTEL_EXPORTER_OTLP_ENDPOINT,
// or _TRACES_ENDPOINT, _METRICS_ENDPOINT), and the counters outbox adds to.
// Without an endpoint, the spans and counters go nowhere.
package telemetry

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

var meter = otel.Meter("github.com/stagdhq/outbox")

// The metrics, for a dashboard of what an app sends: each by the vendor whose
// API the app called (outbox.vendor: resend, ses, sns).
var (
	// Emails are the emails caught, also by what became of them
	// (outbox.outcome): passed on to the SMTP inbox, kept (no SMTP set), or
	// failed (the SMTP inbox refused or couldn't be reached).
	Emails = counter("outbox.emails", "{email}", "Emails the app sent through a vendor's API, caught.")

	// Texts are the text messages caught and kept.
	Texts = counter("outbox.texts", "{text}", "Text messages the app sent through a vendor's API, caught.")

	// SMTPDuration is how long the SMTP inbox took to take an email.
	SMTPDuration = histogram("outbox.smtp.duration", "How long passing an email on to the SMTP inbox took.")
)

// What became of an email, as outbox.outcome.
const (
	Passed = "passed"
	Kept   = "kept"
	Failed = "failed"
)

func counter(name, unit, description string) metric.Int64Counter {
	c, _ := meter.Int64Counter(name, metric.WithUnit(unit), metric.WithDescription(description))

	return c
}

func histogram(name, description string) metric.Float64Histogram {
	h, _ := meter.Float64Histogram(name, metric.WithUnit("s"), metric.WithDescription(description))

	return h
}

// Email counts an email caught, and times its hand-off when it had one.
func Email(vendor, outcome string, started time.Time) {
	ctx := context.Background()
	vendorAttr := attribute.String("outbox.vendor", vendor)

	Emails.Add(ctx, 1, metric.WithAttributes(vendorAttr, attribute.String("outbox.outcome", outcome)))

	if outcome != Kept {
		SMTPDuration.Record(ctx, time.Since(started).Seconds(), metric.WithAttributes(vendorAttr, attribute.String("outbox.outcome", outcome)))
	}
}

// Text counts a text message caught.
func Text(vendor string) {
	Texts.Add(context.Background(), 1, metric.WithAttributes(attribute.String("outbox.vendor", vendor)))
}

type nameKey struct{}

// Name names the request's span by what the app asked, e.g. "AWS
// SendEmail", where its route alone doesn't say.
func Name(ctx context.Context, name string) {
	if held, ok := ctx.Value(nameKey{}).(*string); ok {
		*held = name
	}

	trace.SpanFromContext(ctx).SetName(name)
}

// Handler makes the requests an http.Handler serves spans, joining the
// app's trace if it sent one, each named by Name or else its route ("POST
// /resend/emails"); health checks (/healthz) are left out.
func Handler(h http.Handler) http.Handler {
	traced := otelhttp.NewHandler(h, "outbox",
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz"
		}),
		otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
			if held, ok := r.Context().Value(nameKey{}).(*string); ok && *held != "" {
				return *held
			}

			if r.Pattern != "" {
				return r.Pattern
			}

			return operation
		}),
	)

	// otelhttp names the span again once the request is answered: the name
	// Name gave is kept here for that.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traced.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nameKey{}, new(string))))
	})
}

// Enabled says whether an OTLP endpoint is set, and the SDK not turned off.
func Enabled() bool {
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return false
	}

	for _, key := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"} {
		if os.Getenv(key) != "" {
			return true
		}
	}

	return false
}

// Setup turns telemetry on when Enabled, exporting with the standard OTEL_*
// variables (endpoint, headers, service name, resource attributes). The
// function it returns flushes and stops it.
func Setup(ctx context.Context, version string) (func(context.Context) error, error) {
	if !Enabled() {
		return func(context.Context) error { return nil }, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName("outbox"), semconv.ServiceVersion(version)),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, err
	}

	traces, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}

	metrics, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, err
	}

	tracer := sdktrace.NewTracerProvider(sdktrace.WithBatcher(traces), sdktrace.WithResource(res))
	meters := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metrics)), sdkmetric.WithResource(res))

	otel.SetTracerProvider(tracer)
	otel.SetMeterProvider(meters)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	return func(ctx context.Context) error {
		return errors.Join(tracer.Shutdown(ctx), meters.Shutdown(ctx))
	}, nil
}
