// Copyright 2016 Michal Witkowski. All Rights Reserved.
// See LICENSE for licensing terms.

package grpc_prometheus

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb_testproto "github.com/chronosphereio/go-grpc-prometheus/examples/testproto"
)

var (
	// client metrics must satisfy the Collector interface
	_ prometheus.Collector = NewClientMetrics()
)

func TestClientInterceptorSuite(t *testing.T) {
	suite.Run(t, &ClientInterceptorTestSuite{})
}

type ClientInterceptorTestSuite struct {
	suite.Suite

	serverListener net.Listener
	server         *grpc.Server
	clientConn     *grpc.ClientConn
	testClient     pb_testproto.TestServiceClient
	ctx            context.Context
	cancel         context.CancelFunc
}

func (s *ClientInterceptorTestSuite) SetupSuite() {
	var err error

	EnableClientHandlingTimeHistogram()
	EnableClientMeasureBandwidth()

	s.serverListener, err = net.Listen("tcp", "127.0.0.1:0")
	require.NoError(s.T(), err, "must be able to allocate a port for serverListener")

	// This is the point where we hook up the interceptor
	s.server = grpc.NewServer()
	pb_testproto.RegisterTestServiceServer(s.server, &testService{t: s.T()})

	go func() {
		s.server.Serve(s.serverListener)
	}()

	s.clientConn, err = grpc.Dial(
		s.serverListener.Addr().String(),
		grpc.WithInsecure(),
		grpc.WithBlock(),
		grpc.WithUnaryInterceptor(UnaryClientInterceptor),
		grpc.WithStreamInterceptor(StreamClientInterceptor),
		grpc.WithTimeout(2*time.Second),
		grpc.WithStatsHandler(ClientStatsHandler),
	)
	require.NoError(s.T(), err, "must not error on client Dial")
	s.testClient = pb_testproto.NewTestServiceClient(s.clientConn)
}

func (s *ClientInterceptorTestSuite) SetupTest() {
	// Make all RPC calls last at most 2 sec, meaning all async issues or deadlock will not kill tests.
	s.ctx, s.cancel = context.WithTimeout(context.TODO(), 2*time.Second)

	// Make sure every test starts with same fresh, intialized metric state.
	DefaultClientMetrics.clientStartedCounter.Reset()
	DefaultClientMetrics.clientHandledCounter.Reset()
	DefaultClientMetrics.clientHandledHistogram.Reset()
	DefaultClientMetrics.clientStreamMsgReceived.Reset()
	DefaultClientMetrics.clientStreamMsgSent.Reset()
	DefaultClientMetrics.clientInPayloadByteCounter.Reset()
	DefaultClientMetrics.clientWireInPayloadByteCounter.Reset()
	DefaultClientMetrics.clientOutPayloadByteCounter.Reset()
	DefaultClientMetrics.clientWireOutPayloadByteCounter.Reset()
}

func (s *ClientInterceptorTestSuite) TearDownSuite() {
	if s.serverListener != nil {
		s.server.Stop()
		s.T().Logf("stopped grpc.Server at: %v", s.serverListener.Addr().String())
		s.serverListener.Close()

	}
	if s.clientConn != nil {
		s.clientConn.Close()
	}
}

func (s *ClientInterceptorTestSuite) TearDownTest() {
	s.cancel()
}

func (s *ClientInterceptorTestSuite) TestUnaryIncrementsMetrics() {
	_, err := s.testClient.PingEmpty(s.ctx, &pb_testproto.Empty{}) // should return with code=OK
	require.NoError(s.T(), err)
	requireValue(s.T(), 1, DefaultClientMetrics.clientStartedCounter.WithLabelValues("unary", "mwitkow.testproto.TestService", "PingEmpty"))
	requireValue(s.T(), 1, DefaultClientMetrics.clientHandledCounter.WithLabelValues("unary", "mwitkow.testproto.TestService", "PingEmpty", "OK"))
	requireValueHistCount(s.T(), 1, DefaultClientMetrics.clientHandledHistogram.WithLabelValues("unary", "mwitkow.testproto.TestService", "PingEmpty"))

	_, err = s.testClient.PingError(s.ctx, &pb_testproto.PingRequest{ErrorCodeReturned: uint32(codes.FailedPrecondition)}) // should return with code=FailedPrecondition
	require.Error(s.T(), err)
	requireValue(s.T(), 1, DefaultClientMetrics.clientStartedCounter.WithLabelValues("unary", "mwitkow.testproto.TestService", "PingError"))
	requireValue(s.T(), 1, DefaultClientMetrics.clientHandledCounter.WithLabelValues("unary", "mwitkow.testproto.TestService", "PingError", "FailedPrecondition"))
	requireValueHistCount(s.T(), 1, DefaultClientMetrics.clientHandledHistogram.WithLabelValues("unary", "mwitkow.testproto.TestService", "PingError"))

	// test server byte counters
	_, err = s.testClient.Ping(s.ctx, &pb_testproto.PingRequest{Value: "hello"})
	requireValue(s.T(), 9, DefaultClientMetrics.clientInPayloadByteCounter.WithLabelValues("mwitkow.testproto.TestService", "Ping"))

	// TODO(steve) inpayload wirelength is unfortunately not set in the version of grpc that this project depends on
	// I don't think it's a good idea to go around messing w/ dependencies just because of this.
	// Here's what it looks like now: https://github.com/grpc/grpc-go/blob/master/server.go#L1201
	requireValue(s.T(), 0, DefaultClientMetrics.clientWireInPayloadByteCounter.WithLabelValues("mwitkow.testproto.TestService", "Ping"))

	requireValue(s.T(), 7, DefaultClientMetrics.clientOutPayloadByteCounter.WithLabelValues("mwitkow.testproto.TestService", "Ping"))
	requireValue(s.T(), 12, DefaultClientMetrics.clientWireOutPayloadByteCounter.WithLabelValues("mwitkow.testproto.TestService", "Ping"))
}

func (s *ClientInterceptorTestSuite) TestStartedStreamingIncrementsStarted() {
	_, err := s.testClient.PingList(s.ctx, &pb_testproto.PingRequest{})
	require.NoError(s.T(), err)
	requireValue(s.T(), 1, DefaultClientMetrics.clientStartedCounter.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))

	_, err = s.testClient.PingList(s.ctx, &pb_testproto.PingRequest{ErrorCodeReturned: uint32(codes.FailedPrecondition)}) // should return with code=FailedPrecondition
	require.NoError(s.T(), err, "PingList must not fail immediately")
	requireValue(s.T(), 2, DefaultClientMetrics.clientStartedCounter.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))
}

func (s *ClientInterceptorTestSuite) TestStreamingIncrementsMetrics() {
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

	requireValue(s.T(), 1, DefaultClientMetrics.clientStartedCounter.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))
	requireValue(s.T(), 1, DefaultClientMetrics.clientHandledCounter.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList", "OK"))
	requireValue(s.T(), countListResponses, DefaultClientMetrics.clientStreamMsgReceived.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))
	requireValue(s.T(), 1, DefaultClientMetrics.clientStreamMsgSent.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))
	requireValueHistCount(s.T(), 1, DefaultClientMetrics.clientHandledHistogram.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))

	ss, err := s.testClient.PingList(s.ctx, &pb_testproto.PingRequest{ErrorCodeReturned: uint32(codes.FailedPrecondition)}) // should return with code=FailedPrecondition
	require.NoError(s.T(), err, "PingList must not fail immediately")

	// Do a read, just to progate errors.
	_, err = ss.Recv()
	st, _ := status.FromError(err)
	require.Equal(s.T(), codes.FailedPrecondition, st.Code(), "Recv must return FailedPrecondition, otherwise the test is wrong")

	requireValue(s.T(), 2, DefaultClientMetrics.clientStartedCounter.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))
	requireValue(s.T(), 1, DefaultClientMetrics.clientHandledCounter.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList", "FailedPrecondition"))
	requireValueHistCount(s.T(), 2, DefaultClientMetrics.clientHandledHistogram.WithLabelValues("server_stream", "mwitkow.testproto.TestService", "PingList"))
}
