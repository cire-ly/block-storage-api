package observability

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// MeterProvider wraps the SDK provider with a Close method.
type MeterProvider struct {
	*sdkmetric.MeterProvider
}

func (mp *MeterProvider) Close(ctx context.Context) error {
	return mp.Shutdown(ctx)
}

// NewMeterProvider initialises OTel metrics (stdout exporter).
func NewMeterProvider(ctx context.Context, serviceName string) (*MeterProvider, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	exp, err := stdoutmetric.New()
	if err != nil {
		return nil, fmt.Errorf("stdout metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)),
		sdkmetric.WithResource(res),
	)

	otel.SetMeterProvider(mp)
	return &MeterProvider{mp}, nil
}

// Meter returns a named meter from the global provider.
func Meter(name string) metric.Meter {
	return otel.Meter(name)
}
