// Package twirpotel provides tracing for twirp servers and clients.
package twirpotel

import (
	"context"

	"github.com/twitchtv/twirp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.10.0"
	"go.opentelemetry.io/otel/trace"
)

const (

	// InstrumentationName is the name used for intrumentation in traces.
	InstrumentationName = "github.com/bakins/twirpotel"

	// PackageNameKey is the twirp package name
	PackageNameKey = attribute.Key("twirp.package")

	// ServiceNameKey is the twirp service name
	ServiceNameKey = attribute.Key("twirp.service")

	// MethodNameKey is the twirp method name
	MethodNameKey = attribute.Key("twirp.method")

	// ErrorCodeKey is the twirp error code
	ErrorCodeKey = attribute.Key("twirp.error_code")

	// ErrorMessageKey is the twirp error message
	ErrorMessageKey = attribute.Key("twirp.error_message")
)

// NoErrorCode is the ErrorCode used when there is no error
var NoErrorCode = attribute.StringValue("ok")

type config struct {
	provider trace.TracerProvider
}

func (c config) getTracerProvider(ctx context.Context) trace.TracerProvider {
	if c.provider != nil {
		return c.provider
	}

	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		return span.TracerProvider()
	}

	return otel.GetTracerProvider()
}

// Option applies an option value for a config.
type Option interface {
	apply(*config)
}

type optionFunc func(*config)

func (f optionFunc) apply(c *config) {
	f(c)
}

// WithTracerProvider returns an Option to use the TracerProvider when
// creating a Tracer.
//
// Default is to attempt to get from parent span and then otel.GetTracerProvider().
func WithTracerProvider(provider trace.TracerProvider) Option {
	return optionFunc(func(c *config) {
		c.provider = provider
	})
}

// ServerInterceptor creates interceptors suitable to be used with a twirp server.
func ServerInterceptor(options ...Option) twirp.Interceptor {
	return interceptor(trace.SpanKindServer, options)
}

// ClientInterceptor creates interceptors suitable to be used with twirp client.
func ClientInterceptor(options ...Option) twirp.Interceptor {
	return interceptor(trace.SpanKindClient, options)
}

func interceptor(kind trace.SpanKind, options []Option) twirp.Interceptor {
	var c config

	for _, o := range options {
		o.apply(&c)
	}

	return func(next twirp.Method) twirp.Method {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			tracer := c.getTracerProvider(ctx).Tracer(InstrumentationName)

			fullMethod, attributes := commonAtrributes(ctx)

			ctx, span := tracer.Start(
				ctx,
				fullMethod,
				trace.WithAttributes(attributes...),
				trace.WithSpanKind(kind),
			)

			defer span.End()

			resp, err := next(ctx, req)
			if err != nil {
				span.SetStatus(codes.Error, err.Error())
			}

			span.SetAttributes(getTwirpErrorAttributes(err)...)

			return resp, err
		}
	}
}

func commonAtrributes(ctx context.Context) (string, []attribute.KeyValue) {
	packageName, _ := twirp.PackageName(ctx)
	serviceName, _ := twirp.ServiceName(ctx)
	methodName, _ := twirp.MethodName(ctx)

	fullMethod := packageName + "." + serviceName + "/" + methodName

	return fullMethod, []attribute.KeyValue{
		{
			Key:   PackageNameKey,
			Value: attribute.StringValue(packageName),
		},
		{
			Key:   ServiceNameKey,
			Value: attribute.StringValue(serviceName),
		},
		{
			Key:   MethodNameKey,
			Value: attribute.StringValue(methodName),
		},
		semconv.RPCSystemKey.String("twirp"),
		semconv.RPCMethodKey.String(methodName),
		semconv.RPCServiceKey.String(packageName + "." + serviceName),
	}
}

func getTwirpErrorAttributes(err error) []attribute.KeyValue {
	if err == nil {
		return []attribute.KeyValue{
			{
				Key:   ErrorCodeKey,
				Value: NoErrorCode,
			},
		}
	}

	twerr, ok := err.(twirp.Error)
	if !ok {
		twerr = twirp.InternalErrorWith(err)
	}

	return []attribute.KeyValue{
		{
			Key:   ErrorCodeKey,
			Value: attribute.StringValue(string(twerr.Code())),
		},
		{
			Key:   ErrorMessageKey,
			Value: attribute.StringValue(twerr.Msg()),
		},
	}
}
