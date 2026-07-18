// Package tracing provides OpenTelemetry distributed tracing.
package tracing

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.opentelemetry.io/otel/trace"
)

// Config holds tracing configuration.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Endpoint       string // OTLP gRPC endpoint, e.g. "localhost:4317"
	SampleRatio    float64
}

// Init sets up the global OpenTelemetry tracer provider.
func Init(cfg Config) (func(), error) {
	ctx := context.Background()

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}

	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.Endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		slog.Warn("tracing exporter failed, using noop", "error", err)
		return func() {}, nil
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.SampleRatio)),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	shutdown := func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			slog.Error("tracer provider shutdown failed", "error", err)
		}
	}

	slog.Info("tracing initialized", "service", cfg.ServiceName, "endpoint", cfg.Endpoint)
	return shutdown, nil
}

// Tracer returns the global tracer for the given component.
func Tracer(component string) trace.Tracer {
	return otel.Tracer(component)
}

// HTTPMiddleware continues an incoming W3C trace or starts a new server trace.
func HTTPMiddleware(component string) func(http.Handler) http.Handler {
	tracer := Tracer(component)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			ctx, span := tracer.Start(ctx, r.Method+" "+r.URL.Path,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("http.request.method", r.Method),
					attribute.String("url.path", r.URL.Path),
				),
			)
			defer span.End()

			if traceID := span.SpanContext().TraceID(); traceID.IsValid() {
				w.Header().Set("X-Trace-ID", traceID.String())
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// InjectHTTPHeaders propagates the current W3C trace context to an HTTP request.
func InjectHTTPHeaders(ctx context.Context, req *http.Request) {
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
}
