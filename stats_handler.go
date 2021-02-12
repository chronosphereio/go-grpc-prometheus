package grpc_prometheus

import (
	"context"

	"google.golang.org/grpc/stats"
)

type key int

const (
	fullMethodNameCtxKey key = iota
)

// StatsHandlerOptions control various aspects of what we do with grpc stats-related callbacks
type StatsHandlerOptions struct {
	clientMetrics *ClientMetrics
	serverMetrics *ServerMetrics
}

// NewStatsHandler creates a StatsHandler for use in recording various kinds of grpc metrics.
func NewStatsHandler(opts StatsHandlerOptions) *StatsHandler {
	return &StatsHandler{opts}
}

// StatsHandler is useful for recording various kinds of grpc metrics.
type StatsHandler struct {
	opts StatsHandlerOptions
}

// TagRPC is an implementation of a statshandler callback
func (h StatsHandler) TagRPC(ctx context.Context, s *stats.RPCTagInfo) context.Context {
	// see https://stackoverflow.com/questions/50415796/measure-grpc-bandwidth-per-stream#comment88001066_50426301
	return context.WithValue(ctx, fullMethodNameCtxKey, s.FullMethodName)
}

// HandleRPC is an implementation of a statshandler callback
func (h StatsHandler) HandleRPC(ctx context.Context, s stats.RPCStats) {
	if h.opts.serverMetrics != nil && h.opts.serverMetrics.serverMeasureBandwidthEnabled && !s.IsClient() {
		fullMethodName := ctx.Value(fullMethodNameCtxKey).(string)
		serviceName, methodName := splitMethodName(fullMethodName)

		switch v := s.(type) {
		case *stats.InPayload:
			h.opts.serverMetrics.serverInPayloadByteCounter.WithLabelValues(serviceName, methodName).Add(float64(v.Length))
			h.opts.serverMetrics.serverWireInPayloadByteCounter.WithLabelValues(serviceName, methodName).Add(float64(v.WireLength))
		case *stats.OutPayload:
			h.opts.serverMetrics.serverOutPayloadByteCounter.WithLabelValues(serviceName, methodName).Add(float64(v.Length))
			h.opts.serverMetrics.serverWireOutPayloadByteCounter.WithLabelValues(serviceName, methodName).Add(float64(v.WireLength))
		}
	}

	if h.opts.clientMetrics != nil && h.opts.clientMetrics.clientHandledHistogramEnabled && s.IsClient() {
		fullMethodName := ctx.Value(fullMethodNameCtxKey).(string)
		serviceName, methodName := splitMethodName(fullMethodName)

		switch v := s.(type) {
		case *stats.InPayload:
			h.opts.clientMetrics.clientInPayloadByteCounter.WithLabelValues(serviceName, methodName).Add(float64(v.Length))
			h.opts.clientMetrics.clientWireInPayloadByteCounter.WithLabelValues(serviceName, methodName).Add(float64(v.WireLength))
		case *stats.OutPayload:
			h.opts.clientMetrics.clientOutPayloadByteCounter.WithLabelValues(serviceName, methodName).Add(float64(v.Length))
			h.opts.clientMetrics.clientWireOutPayloadByteCounter.WithLabelValues(serviceName, methodName).Add(float64(v.WireLength))
		}
	}
}

// TagConn is an implementation of a statshandler callback
func (h StatsHandler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	// noop
	return ctx
}

// HandleConn is an implementation of a statshandler callback
func (h StatsHandler) HandleConn(ctx context.Context, s stats.ConnStats) {
	// noop
}
