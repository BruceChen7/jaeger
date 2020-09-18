// Copyright (c) 2017 Uber Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package app

import (
	"fmt"
	"net/http"

	"github.com/apache/thrift/lib/go/thrift"
	"github.com/pkg/errors"
	"github.com/uber/jaeger-lib/metrics"
	"go.uber.org/zap"

	"github.com/jaegertracing/jaeger/cmd/agent/app/configmanager"
	"github.com/jaegertracing/jaeger/cmd/agent/app/httpserver"
	"github.com/jaegertracing/jaeger/cmd/agent/app/processors"
	"github.com/jaegertracing/jaeger/cmd/agent/app/reporter"
	"github.com/jaegertracing/jaeger/cmd/agent/app/servers"
	"github.com/jaegertracing/jaeger/cmd/agent/app/servers/thriftudp"
	zipkinThrift "github.com/jaegertracing/jaeger/thrift-gen/agent"
	jaegerThrift "github.com/jaegertracing/jaeger/thrift-gen/jaeger"
)

const (
	defaultQueueSize     = 1000
	defaultMaxPacketSize = 65000
	defaultServerWorkers = 10

	defaultHTTPServerHostPort = ":5778"

	jaegerModel Model = "jaeger"
	zipkinModel       = "zipkin"

	compactProtocol Protocol = "compact"
	binaryProtocol           = "binary"
)

// Model used to distinguish the data transfer model
type Model string

// Protocol used to distinguish the data transfer protocol
type Protocol string

var (
	errNoReporters = errors.New("agent requires at least one Reporter")

	protocolFactoryMap = map[Protocol]thrift.TProtocolFactory{
		compactProtocol: thrift.NewTCompactProtocolFactory(),
		binaryProtocol:  thrift.NewTBinaryProtocolFactoryDefault(),
	}
)

// CollectorProxy provides access to Reporter and ClientConfigManager
// 获取所有到reporter
type CollectorProxy interface {
	GetReporter() reporter.Reporter
	GetManager() configmanager.ClientConfigManager
}

// Builder Struct to hold configurations
type Builder struct {
    // 后端处理协议到处理器
	Processors []ProcessorConfiguration `yaml:"processors"`
	HTTPServer HTTPServerConfiguration  `yaml:"httpServer"`

	reporters []reporter.Reporter
}

// ProcessorConfiguration holds config for a processor that receives spans from Server
type ProcessorConfiguration struct {
	Workers  int                 `yaml:"workers"`
	Model    Model               `yaml:"model"`
	Protocol Protocol            `yaml:"protocol"`
    // server的配置
	Server   ServerConfiguration `yaml:"server"`
}

// ServerConfiguration holds config for a server that receives spans from the network
type ServerConfiguration struct {
	QueueSize     int    `yaml:"queueSize"`
	MaxPacketSize int    `yaml:"maxPacketSize"`
	HostPort      string `yaml:"hostPort" validate:"nonzero"`
}

// HTTPServerConfiguration holds config for a server providing sampling strategies and baggage restrictions to clients
type HTTPServerConfiguration struct {
	HostPort string `yaml:"hostPort" validate:"nonzero"`
}

// WithReporter adds auxiliary reporters.
func (b *Builder) WithReporter(r ...reporter.Reporter) *Builder {
	b.reporters = append(b.reporters, r...)
	return b
}

// CreateAgent creates the Agent
func (b *Builder) CreateAgent(primaryProxy CollectorProxy, logger *zap.Logger, mFactory metrics.Factory) (*Agent, error) {
    // 获取上报者
	r := b.getReporter(primaryProxy)
    // processors
	processors, err := b.getProcessors(r, mFactory, logger)
	if err != nil {
		return nil, err
	}
	server := b.HTTPServer.getHTTPServer(primaryProxy.GetManager(), mFactory)
    // 创建agent
	return NewAgent(processors, server, logger), nil
}

func (b *Builder) getReporter(primaryProxy CollectorProxy) reporter.Reporter {
    // 配置中如果reportor如果为0
	if len(b.reporters) == 0 {
        // 从proxy中获取
		return primaryProxy.GetReporter()
	}
    //  创建
	rep := make([]reporter.Reporter, len(b.reporters)+1)
	rep[0] = primaryProxy.GetReporter()
	for i, r := range b.reporters {
		rep[i+1] = r
	}
    // 创建多个reportor
	return reporter.NewMultiReporter(rep...)
}

func (b *Builder) getProcessors(rep reporter.Reporter, mFactory metrics.Factory, logger *zap.Logger) ([]processors.Processor, error) {
	retMe := make([]processors.Processor, len(b.Processors))
	for idx, cfg := range b.Processors {
		protoFactory, ok := protocolFactoryMap[cfg.Protocol]
		if !ok {
			return nil, fmt.Errorf("cannot find protocol factory for protocol %v", cfg.Protocol)
		}
		var handler processors.AgentProcessor
		switch cfg.Model {
         // 根据数据模型，创建不同的handler
		case jaegerModel:
			handler = jaegerThrift.NewAgentProcessor(rep)
		case zipkinModel:
			handler = zipkinThrift.NewAgentProcessor(rep)
		default:
			return nil, fmt.Errorf("cannot find agent processor for data model %v", cfg.Model)
		}
        // 上报的metrics
		metrics := mFactory.Namespace(metrics.NSOptions{Name: "", Tags: map[string]string{
			"protocol": string(cfg.Protocol),
			"model":    string(cfg.Model),
		}})
		processor, err := cfg.GetThriftProcessor(metrics, protoFactory, handler, logger)
		if err != nil {
			return nil, err
		}
		retMe[idx] = processor
	}
	return retMe, nil
}

// GetHTTPServer creates an HTTP server that provides sampling strategies and baggage restrictions to client libraries.
func (c HTTPServerConfiguration) getHTTPServer(manager configmanager.ClientConfigManager, mFactory metrics.Factory) *http.Server {
	if c.HostPort == "" {
		c.HostPort = defaultHTTPServerHostPort
	}
	return httpserver.NewHTTPServer(c.HostPort, manager, mFactory)
}

// GetThriftProcessor gets a TBufferedServer backed Processor using the collector configuration
func (c *ProcessorConfiguration) GetThriftProcessor(
	mFactory metrics.Factory,
	factory thrift.TProtocolFactory,
	handler processors.AgentProcessor,
	logger *zap.Logger,
) (processors.Processor, error) {
    // 配置works
	c.applyDefaults()

    // 根据Server的配置创建一个udp server
	server, err := c.Server.getUDPServer(mFactory)
	if err != nil {
		return nil, err
	}

    // 创建thrift数据处理器
	return processors.NewThriftProcessor(server, c.Workers, mFactory, factory, handler, logger)
}

func (c *ProcessorConfiguration) applyDefaults() {
    // 默认10个worker
	c.Workers = defaultInt(c.Workers, defaultServerWorkers)
}

func (c *ServerConfiguration) applyDefaults() {
	c.QueueSize = defaultInt(c.QueueSize, defaultQueueSize)
	c.MaxPacketSize = defaultInt(c.MaxPacketSize, defaultMaxPacketSize)
}

// getUDPServer gets a TBufferedServer backed server using the server configuration
func (c *ServerConfiguration) getUDPServer(mFactory metrics.Factory) (servers.Server, error) {
    // 使用默认的配置
	c.applyDefaults()

	if c.HostPort == "" {
		return nil, fmt.Errorf("no host:port provided for udp server: %+v", *c)
	}
    // 创建udp端口
	transport, err := thriftudp.NewTUDPServerTransport(c.HostPort)
	if err != nil {
		return nil, err
	}

    // 创建一个udp server
	return servers.NewTBufferedServer(transport, c.QueueSize, c.MaxPacketSize, mFactory)
}

func defaultInt(value int, defaultVal int) int {
	if value == 0 {
		value = defaultVal
	}
	return value
}
