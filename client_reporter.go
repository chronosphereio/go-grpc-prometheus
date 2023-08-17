// Copyright 2016 Michal Witkowski. All Rights Reserved.
// See LICENSE for licensing terms.

package grpc_prometheus

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
)

type clientReporter struct {
	metrics     *ClientMetrics
	rpcType     grpcType
	serviceName string
	methodName  string
	startTime   time.Time
}

func newClientReporter(m *ClientMetrics, rpcType grpcType, fullMethod string) *clientReporter {
	r := &clientReporter{
		metrics: m,
		rpcType: rpcType,
	}
	if r.metrics.clientHandledHistogramEnabled {
		r.startTime = time.Now()
	}
	r.serviceName, r.methodName = splitMethodName(fullMethod)
	r.metrics.clientStartedCounter.WithLabelValues(string(r.rpcType), r.serviceName, r.methodName).Inc()
	return r
}

// timer is a helper interface to time functions.
type timer interface {
	ObserveDuration() time.Duration
}

type histogramTimer struct {
	begin    time.Time
	observer prometheus.Observer
}

func newHistogramTimer(observer prometheus.Observer) histogramTimer {
	return histogramTimer{
		begin:    time.Now(),
		observer: observer,
	}
}

func (t histogramTimer) ObserveDuration() time.Duration {
	d := time.Since(t.begin)
	if t.observer != nil {
		t.observer.Observe(d.Seconds())
	}
	return d
}

type noOpTimer struct {
}

func (noOpTimer) ObserveDuration() time.Duration {
	return 0
}

var emptyTimer = noOpTimer{}

func (r *clientReporter) ReceiveMessageTimer() timer {
	if r.metrics.clientStreamRecvHistogramEnabled {
		hist := r.metrics.clientStreamRecvHistogram.WithLabelValues(string(r.rpcType), r.serviceName, r.methodName)
		return newHistogramTimer(hist)
	}

	return emptyTimer
}

func (r *clientReporter) ReceivedMessage() {
	r.metrics.clientStreamMsgReceived.WithLabelValues(string(r.rpcType), r.serviceName, r.methodName).Inc()
}

func (r *clientReporter) SendMessageTimer() timer {
	if r.metrics.clientStreamSendHistogramEnabled {
		hist := r.metrics.clientStreamSendHistogram.WithLabelValues(string(r.rpcType), r.serviceName, r.methodName)
		return newHistogramTimer(hist)
	}

	return emptyTimer
}

func (r *clientReporter) SentMessage() {
	r.metrics.clientStreamMsgSent.WithLabelValues(string(r.rpcType), r.serviceName, r.methodName).Inc()
}

func (r *clientReporter) Handled(code codes.Code) {
	r.metrics.clientHandledCounter.WithLabelValues(string(r.rpcType), r.serviceName, r.methodName, code.String()).Inc()
	if r.metrics.clientHandledHistogramEnabled {
		r.metrics.clientHandledHistogram.WithLabelValues(string(r.rpcType), r.serviceName, r.methodName).Observe(time.Since(r.startTime).Seconds())
	}
}
