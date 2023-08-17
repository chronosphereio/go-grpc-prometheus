// Copyright 2016 Michal Witkowski. All Rights Reserved.
// See LICENSE for licensing terms.

package grpc_prometheus

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb_testproto "github.com/chronosphereio/go-grpc-prometheus/examples/testproto"
)

var (
	// server metrics must satisfy the Collector interface
	_ prometheus.Collector = NewServerMetrics()
)

const (
	pingDefaultValue   = "I like kittens."
	countListResponses = 20
)

func TestServerInterceptorSuite(t *testing.T) {
	suite.Run(t, &ServerInterceptorTestSuite{})
}

type ServerInterceptorTestSuite struct {
	suite.Suite

	serverListener net.Listener
	server         *grpc.Server
	clientConn     *grpc.ClientConn
	testClient     pb_testproto.TestServiceClient
	ctx            context.Context
	cancel         context.CancelFunc
}

func (s *ServerInterceptorTestSuite) SetupSuite() {
	var err error

	EnableHandlingTimeHistogram()
	EnableMeasureBandwidth()

	s.serverListener, err = net.Listen("tcp", "127.0.0.1:0")
	require.NoError(s.T(), err, "must be able to allocate a port for serverListener")

	// This is the point where we hook up the interceptor and stats handler
	s.server = grpc.NewServer(
		grpc.StreamInterceptor(StreamServerInterceptor),
		grpc.UnaryInterceptor(UnaryServerInterceptor),
		grpc.StatsHandler(ServerStatsHandler),
	)
	pb_testproto.RegisterTestServiceServer(s.server, &testService{t: s.T()})

	go func() {
		s.server.Serve(s.serverListener)
	}()

	s.clientConn, err = grpc.Dial(s.serverListener.Addr().String(), grpc.WithInsecure(), grpc.WithBlock(), grpc.WithTimeout(2*time.Second))
	require.NoError(s.T(), err, "must not error on client Dial")
	s.testClient = pb_testproto.NewTestServiceClient(s.clientConn)
}

func (s *ServerInterceptorTestSuite) SetupTest() {
	// Make all RPC calls last at most 2 sec, meaning all async issues or deadlock will not kill tests.
	s.ctx, s.cancel = context.WithTimeout(context.TODO(), 2*time.Second)

	// Make sure every test starts with same fresh, intialized metric state.
	DefaultServerMetrics.serverStartedCounter.Reset()
	DefaultServerMetrics.serverHandledCounter.Reset()
	DefaultServerMetrics.serverHandledHistogram.Reset()
	DefaultServerMetrics.serverStreamMsgReceived.Reset()
	DefaultServerMetrics.serverStreamMsgSent.Reset()
	DefaultServerMetrics.serverInPayloadByteCounter.Reset()
	DefaultServerMetrics.serverWireInPayloadByteCounter.Reset()
	DefaultServerMetrics.serverOutPayloadByteCounter.Reset()
	DefaultServerMetrics.serverWireOutPayloadByteCounter.Reset()
	Register(s.server)
}

func (s *ServerInterceptorTestSuite) TearDownSuite() {
	if s.serverListener != nil {
		s.server.Stop()
		s.T().Logf("stopped grpc.Server at: %v", s.serverListener.Addr().String())
		s.serverListener.Close()

	}
	if s.clientConn != nil {
		s.clientConn.Close()
	}
}

func (s *ServerInterceptorTestSuite) TearDownTest() {
	s.cancel()
}

func (s *ServerInterceptorTestSuite) TestRegisterPresetsStuff() {
	for testID, testCase := range []struct {
		metricName     string
		existingLabels []string
	}{
		// Order of label is irrelevant.
		{"grpc_server_started_total", []string{"mwitkow.testproto.TestService", "PingEmpty", "unary"}},
		{"grpc_server_started_total", []string{"mwitkow.testproto.TestService", "PingList", "server_stream"}},
		{"grpc_server_msg_received_total", []string{"mwitkow.testproto.TestService", "PingList", "server_stream"}},
		{"grpc_server_msg_sent_total", []string{"mwitkow.testproto.TestService", "PingEmpty", "unary"}},
		{"grpc_server_handling_seconds_sum", []string{"mwitkow.testproto.TestService", "PingEmpty", "unary"}},
		{"grpc_server_handling_seconds_count", []string{"mwitkow.testproto.TestService", "PingList", "server_stream"}},
		{"grpc_server_handled_total", []string{"mwitkow.testproto.TestService", "PingList", "server_stream", "OutOfRange"}},
		{"grpc_server_handled_total", []string{"mwitkow.testproto.TestService", "PingList", "server_stream", "Aborted"}},
		{"grpc_server_handled_total", []string{"mwitkow.testproto.TestService", "PingEmpty", "unary", "FailedPrecondition"}},
		{"grpc_server_handled_total", []string{"mwitkow.testproto.TestService", "PingEmpty", "unary", "ResourceExhausted"}},
		{"grpc_server_in_payload_bytes", []string{"mwitkow.testproto.TestService", "Ping"}},
		{"grpc_server_wire_in_payload_bytes", []string{"mwitkow.testproto.TestService", "Ping"}},
		{"grpc_server_out_payload_bytes", []string{"mwitkow.testproto.TestService", "Ping"}},
		{"grpc_server_wire_out_payload_bytes", []string{"mwitkow.testproto.TestService", "Ping"}},
	} {
		lineCount := len(fetchPrometheusLines(s.T(), testCase.metricName, testCase.existingLabels...))
		assert.NotEqual(s.T(), 0, lineCount, "metrics must exist for test case %d", testID)
	}
}

func (s *ServerInterceptorTestSuite) TestUnaryIncrementsMetrics() {
	_, err := s.testClient.PingEmpty(s.ctx, &pb_testproto.Empty{}) // should return with code=OK
	require.NoError(s.T(), err)
	requireValue(s.T(), 1, DefaultServerMetrics.serverStartedCounter.WithLabelValues("unary", "mwitkow.testproto.TestService", "PingEmpty"))
	requireValue(s.T(), 1, DefaultServerMetrics.serverHandledCounter.WithLabelValues("unary", "mwitkow.testproto.TestService", "PingEmpty", "OK"))
	requireValueHistCount(s.T(), 1, DefaultServerMetrics.serverHandledHistogram.WithLabelValues("unary", "mwitkow.testproto.TestService", "PingEmpty"))

	_, err = s.testClient.PingError(s.ctx, &pb_testproto.PingRequest{ErrorCodeReturned: uint32(codes.FailedPrecondition)}) // should return with code=FailedPrecondition
	require.Error(s.T(), err)
	requireValue(s.T(), 1, DefaultServerMetrics.serverStartedCounter.WithLabelValues("unary", "mwitkow.testproto.TestService", "PingError"))
	requireValue(s.T(), 1, DefaultServerMetrics.serverHandledCounter.WithLabelValues("unary", "mwitkow.testproto.TestService", "PingError", "FailedPrecondition"))
	requireValueHistCount(s.T(), 1, DefaultServerMetrics.serverHandledHistogram.WithLabelValues("unary", "mwitkow.testproto.TestService", "PingError"))

	// test server byte counters
	_, err = s.testClient.Ping(s.ctx, &pb_testproto.PingRequest{Value: "hello"})
	requireValue(s.T(), 7, DefaultServerMetrics.serverInPayloadByteCounter.WithLabelValues("mwitkow.testproto.TestService", "Ping"))

	// TODO(steve) inpayload wirelength is unfortunately not set in the version of grpc that this project depends on
	// I don't think it's a good idea to go around messing w/ dependencies just because of this.
	// Here's what it looks like now: https://github.com/grpc/grpc-go/blob/master/server.go#L1201
	requireValue(s.T(), 0, DefaultServerMetrics.serverWireInPayloadByteCounter.WithLabelValues("mwitkow.testproto.TestService", "Ping"))

	requireValue(s.T(), 9, DefaultServerMetrics.serverOutPayloadByteCounter.WithLabelValues("mwitkow.testproto.TestService", "Ping"))
	requireValue(s.T(), 14, DefaultServerMetrics.serverWireOutPayloadByteCounter.WithLabelValues("mwitkow.testproto.TestService", "Ping"))
}

func (s *ServerInterceptorTestSuite) TestStartedStreamingIncrementsStarted() {
	_, err := s.testClient.PingList(s.ctx, &pb_testproto.PingRequest{})
	require.NoError(s.T(), err)
	requireValueWithRetry(s.ctx, s.T(), 1,
		DefaultServerMetrics.serverStartedCounter.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))

	_, err = s.testClient.PingList(s.ctx, &pb_testproto.PingRequest{ErrorCodeReturned: uint32(codes.FailedPrecondition)}) // should return with code=FailedPrecondition
	require.NoError(s.T(), err, "PingList must not fail immediately")
	requireValueWithRetry(s.ctx, s.T(), 2,
		DefaultServerMetrics.serverStartedCounter.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))
}

func (s *ServerInterceptorTestSuite) TestStreamingIncrementsMetrics() {
	ss, _ := s.testClient.PingList(s.ctx, &pb_testproto.PingRequest{}) // should return with code=OK
	// Do a read, just for kicks.
	count := 0
	for {
		_, err := ss.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(s.T(), err, "reading pingList shouldn't fail")
		count++
	}
	require.EqualValues(s.T(), countListResponses, count, "Number of received msg on the wire must match")

	requireValueWithRetry(s.ctx, s.T(), 1,
		DefaultServerMetrics.serverStartedCounter.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))
	requireValueWithRetry(s.ctx, s.T(), 1,
		DefaultServerMetrics.serverHandledCounter.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList", "OK"))
	requireValueWithRetry(s.ctx, s.T(), countListResponses,
		DefaultServerMetrics.serverStreamMsgSent.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))
	requireValueWithRetry(s.ctx, s.T(), 1,
		DefaultServerMetrics.serverStreamMsgReceived.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))
	requireValueWithRetryHistCount(s.ctx, s.T(), 1,
		DefaultServerMetrics.serverHandledHistogram.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))

	_, err := s.testClient.PingList(s.ctx, &pb_testproto.PingRequest{ErrorCodeReturned: uint32(codes.FailedPrecondition)}) // should return with code=FailedPrecondition
	require.NoError(s.T(), err, "PingList must not fail immediately")

	requireValueWithRetry(s.ctx, s.T(), 2,
		DefaultServerMetrics.serverStartedCounter.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))
	requireValueWithRetry(s.ctx, s.T(), 1,
		DefaultServerMetrics.serverHandledCounter.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList", "FailedPrecondition"))
	requireValueWithRetryHistCount(s.ctx, s.T(), 2,
		DefaultServerMetrics.serverHandledHistogram.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))
}

// fetchPrometheusLines does mocked HTTP GET request against real prometheus handler to get the same view that Prometheus
// would have while scraping this endpoint.
// Order of matching label vales does not matter.
func fetchPrometheusLines(t *testing.T, metricName string, matchingLabelValues ...string) []string {
	resp := httptest.NewRecorder()
	req, err := http.NewRequest("GET", "/", nil)
	require.NoError(t, err, "failed creating request for Prometheus handler")

	promhttp.Handler().ServeHTTP(resp, req)
	reader := bufio.NewReader(resp.Body)

	var ret []string
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		} else {
			require.NoError(t, err, "error reading stuff")
		}
		if !strings.HasPrefix(line, metricName) {
			continue
		}
		matches := true
		for _, labelValue := range matchingLabelValues {
			if !strings.Contains(line, `"`+labelValue+`"`) {
				matches = false
			}
		}
		if matches {
			ret = append(ret, line)
		}

	}
	return ret
}

type testService struct {
	t *testing.T
}

func (s *testService) PingEmpty(ctx context.Context, _ *pb_testproto.Empty) (*pb_testproto.PingResponse, error) {
	return &pb_testproto.PingResponse{Value: pingDefaultValue, Counter: 42}, nil
}

func (s *testService) Ping(ctx context.Context, ping *pb_testproto.PingRequest) (*pb_testproto.PingResponse, error) {
	// Send user trailers and headers.
	return &pb_testproto.PingResponse{Value: ping.Value, Counter: 42}, nil
}

func (s *testService) PingError(ctx context.Context, ping *pb_testproto.PingRequest) (*pb_testproto.Empty, error) {
	code := codes.Code(ping.ErrorCodeReturned)
	return nil, status.Errorf(code, "Userspace error.")
}

func (s *testService) PingList(ping *pb_testproto.PingRequest, stream pb_testproto.TestService_PingListServer) error {
	if ping.ErrorCodeReturned != 0 {
		return status.Errorf(codes.Code(ping.ErrorCodeReturned), "foobar")
	}
	// Send user trailers and headers.
	for i := 0; i < countListResponses; i++ {
		stream.Send(&pb_testproto.PingResponse{Value: ping.Value, Counter: int32(i)})
	}
	return nil
}

// Observer is the interface that wraps the Observe method, which is used by
// Histogram and Summary to add observations.
type Observer interface {
	Observe(float64)
}

// toFloat64HistCount does the same thing as prometheus go client ToFloat64, but for histograms.
// TODO(bwplotka): Upstream this function to prometheus client.
func toFloat64HistCount(h Observer) uint64 {
	var (
		m      prometheus.Metric
		mCount int
		mChan  = make(chan prometheus.Metric)
		done   = make(chan struct{})
	)

	go func() {
		for m = range mChan {
			mCount++
		}
		close(done)
	}()

	c, ok := h.(prometheus.Collector)
	if !ok {
		panic(fmt.Errorf("observer is not a collector; got: %T", h))
	}

	c.Collect(mChan)
	close(mChan)
	<-done

	if mCount != 1 {
		panic(fmt.Errorf("collected %d metrics instead of exactly 1", mCount))
	}

	pb := &dto.Metric{}
	m.Write(pb)
	if pb.Histogram != nil {
		return pb.Histogram.GetSampleCount()
	}
	panic(fmt.Errorf("collected a non-histogram metric: %s", pb))
}

func requireValue(t *testing.T, expect int, c prometheus.Collector) {
	v := int(ToFloat64(c))
	if v == expect {
		return
	}

	metricFullName := reflect.ValueOf(*c.(prometheus.Metric).Desc()).FieldByName("fqName").String()
	t.Errorf("expected %d %s value; got %d; ", expect, metricFullName, v)
	t.Fail()
}

func requireValueHistCount(t *testing.T, expect int, o Observer) {
	v := int(toFloat64HistCount(o))
	if v == expect {
		return
	}

	metricFullName := reflect.ValueOf(*o.(prometheus.Metric).Desc()).FieldByName("fqName").String()
	t.Errorf("expected %d %s value; got %d; ", expect, metricFullName, v)
	t.Fail()
}

func requireValueWithRetry(ctx context.Context, t *testing.T, expect int, c prometheus.Collector) {
	for {
		v := int(ToFloat64(c))
		if v == expect {
			return
		}

		select {
		case <-ctx.Done():
			metricFullName := reflect.ValueOf(*c.(prometheus.Metric).Desc()).FieldByName("fqName").String()
			t.Errorf("timeout while expecting %d %s value; got %d; ", expect, metricFullName, v)
			t.Fail()
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func requireValueWithRetryHistCount(ctx context.Context, t *testing.T, expect int, o Observer) {
	for {
		v := int(toFloat64HistCount(o))
		if v == expect {
			return
		}

		select {
		case <-ctx.Done():
			metricFullName := reflect.ValueOf(*o.(prometheus.Metric).Desc()).FieldByName("fqName").String()
			t.Errorf("timeout while expecting %d %s histogram count value; got %d; ", expect, metricFullName, v)
			t.Fail()
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// ToFloat64 collects all Metrics from the provided Collector. It expects that
// this results in exactly one Metric being collected, which must be a Gauge,
// Counter, or Untyped. In all other cases, ToFloat64 panics. ToFloat64 returns
// the value of the collected Metric.
//
// The Collector provided is typically a simple instance of Gauge or Counter, or
// – less commonly – a GaugeVec or CounterVec with exactly one element. But any
// Collector fulfilling the prerequisites described above will do.
//
// Use this function with caution. It is computationally very expensive and thus
// not suited at all to read values from Metrics in regular code. This is really
// only for testing purposes, and even for testing, other approaches are often
// more appropriate (see this package's documentation).
//
// A clear anti-pattern would be to use a metric type from the prometheus
// package to track values that are also needed for something else than the
// exposition of Prometheus metrics. For example, you would like to track the
// number of items in a queue because your code should reject queuing further
// items if a certain limit is reached. It is tempting to track the number of
// items in a prometheus.Gauge, as it is then easily available as a metric for
// exposition, too. However, then you would need to call ToFloat64 in your
// regular code, potentially quite often. The recommended way is to track the
// number of items conventionally (in the way you would have done it without
// considering Prometheus metrics) and then expose the number with a
// prometheus.GaugeFunc.
func ToFloat64(c prometheus.Collector) float64 {
	var (
		m      prometheus.Metric
		mCount int
		mChan  = make(chan prometheus.Metric)
		done   = make(chan struct{})
	)

	go func() {
		for m = range mChan {
			mCount++
		}
		close(done)
	}()

	c.Collect(mChan)
	close(mChan)
	<-done

	if mCount != 1 {
		panic(fmt.Errorf("collected %d metrics instead of exactly 1", mCount))
	}

	pb := &dto.Metric{}
	m.Write(pb)
	if pb.Gauge != nil {
		return pb.Gauge.GetValue()
	}
	if pb.Counter != nil {
		return pb.Counter.GetValue()
	}
	if pb.Untyped != nil {
		return pb.Untyped.GetValue()
	}
	panic(fmt.Errorf("collected a non-gauge/counter/untyped metric: %s", pb))
}
