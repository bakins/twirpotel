package twirpotel_test

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/twitchtv/twirp"
	"github.com/twitchtv/twirp/example"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace"
	spanTrace "go.opentelemetry.io/otel/trace"

	"github.com/bakins/twirpotel"
)

func ExampleServerInterceptor() {
	// Add the server interceptor to your twirp server
	ts := example.NewHaberdasherServer(
		&randomHaberdasher{}, // implements example.Haberdasher
		twirp.WithServerInterceptors(twirpotel.ServerInterceptor()),
	)

	http.Handle(ts.PathPrefix(), ts)
}

type randomHaberdasher struct{}

func (h *randomHaberdasher) MakeHat(ctx context.Context, size *example.Size) (*example.Hat, error) {
	if size.Inches <= 0 {
		return nil, twirp.InvalidArgumentError("Inches", "I can't make a hat that small!")
	}
	colors := []string{"white", "black", "brown", "red", "blue"}
	names := []string{"bowler", "baseball cap", "top hat", "derby"}
	return &example.Hat{
		Size:  size.Inches,
		Color: colors[rand.Intn(len(colors))],
		Name:  names[rand.Intn(len(names))],
	}, nil
}

func TestInterceptors(t *testing.T) {
	tests := map[string]struct {
		errorCode       twirp.ErrorCode
		expectedCode    string
		expectedMessage string
	}{
		"ok": {
			errorCode:    twirp.NoError,
			expectedCode: twirpotel.NoErrorCode.AsString(),
		},
		"invalid argument": {
			errorCode:       twirp.InvalidArgument,
			expectedCode:    string(twirp.InvalidArgument),
			expectedMessage: string(twirp.InvalidArgument),
		},
		"internal": {
			errorCode:       "create",
			expectedCode:    string(twirp.Internal),
			expectedMessage: "invalid error type create",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var exp exporter

			provider := trace.NewTracerProvider(
				trace.WithSyncer(&exp),
			)

			old := otel.GetTracerProvider()

			otel.SetTracerProvider(provider)

			defer func() {
				otel.SetTracerProvider(old)
			}()

			svr := httptest.NewServer(
				example.NewHaberdasherServer(
					&server{errorCode: test.errorCode},
					twirp.WithServerInterceptors(twirpotel.ServerInterceptor()),
				),
			)
			defer svr.Close()

			client := example.NewHaberdasherProtobufClient(
				svr.URL,
				http.DefaultClient,
				twirp.WithClientInterceptors(twirpotel.ClientInterceptor()),
			)

			resp, err := client.MakeHat(context.Background(), &example.Size{})
			if test.errorCode == twirp.NoError {
				require.NoError(t, err)
				require.NotEmpty(t, resp)
			} else {
				require.Error(t, err)
			}

			require.Len(t, exp.spans, 2)

			var foundClient, foundServer bool

			for _, span := range exp.spans {
				switch span.SpanKind() {
				case spanTrace.SpanKindServer:
					foundServer = true
				case spanTrace.SpanKindClient:
					foundClient = true
				default:
					t.Errorf("unexpected span kind %v", span.SpanKind())
					return
				}
				requireAttribute(t, span.Attributes(), twirpotel.PackageNameKey, "twitch.twirp.example")
				requireAttribute(t, span.Attributes(), twirpotel.ServiceNameKey, "Haberdasher")
				requireAttribute(t, span.Attributes(), twirpotel.MethodNameKey, "MakeHat")
				requireAttribute(t, span.Attributes(), twirpotel.ErrorCodeKey, test.expectedCode)

				if test.expectedCode != twirpotel.NoErrorCode.AsString() {
					status := span.Status()
					require.Equal(t, codes.Error, status.Code)
				}

				if test.expectedMessage != "" {
					requireAttribute(t, span.Attributes(), twirpotel.ErrorMessageKey, test.expectedMessage)
				}
			}

			require.True(t, foundClient)
			require.True(t, foundServer)
		})
	}
}

func requireAttribute(t *testing.T, attributes []attribute.KeyValue, key attribute.Key, value string) {
	t.Helper()
	found := false

	for _, a := range attributes {
		if string(a.Key) != string(key) {
			continue
		}

		found = true

		require.Equal(t, value, a.Value.AsString())
		// only check first value
		break
	}

	require.True(t, found, "did not find attribute %s", string(key))
}

type server struct {
	errorCode twirp.ErrorCode
}

func (s *server) MakeHat(context.Context, *example.Size) (*example.Hat, error) {
	if s.errorCode != twirp.NoError {
		if string(s.errorCode) == "testing" {
			return nil, errors.New("testing")
		}

		return nil, twirp.NewError(s.errorCode, string(s.errorCode))
	}

	return &example.Hat{}, nil
}

type exporter struct {
	lock  sync.Mutex
	spans []trace.ReadOnlySpan
}

func (e *exporter) ExportSpans(_ context.Context, spans []trace.ReadOnlySpan) error {
	e.lock.Lock()
	defer e.lock.Unlock()

	e.spans = append(e.spans, spans...)
	return nil
}

func (e *exporter) Shutdown(_ context.Context) error {
	return nil
}

var _ trace.SpanExporter = &exporter{}
