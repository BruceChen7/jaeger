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
	"io"
	"net"
	"net/http"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/jaegertracing/jaeger/cmd/agent/app/processors"
)

// Agent is a composition of all services / components
type Agent struct {
    // 几个端口的处理器
	processors []processors.Processor
    // httpSever
	httpServer *http.Server
    // 设置http地址
	httpAddr   atomic.Value // string, set once agent starts listening
	logger     *zap.Logger
	closer     io.Closer
}

// NewAgent creates the new Agent.
func NewAgent(
	processors []processors.Processor,
	httpServer *http.Server,
	logger *zap.Logger,
) *Agent {
	a := &Agent{
		processors: processors,
		httpServer: httpServer,
		logger:     logger,
	}
	a.httpAddr.Store("")
	return a
}

// GetServer returns HTTP server used by the agent.
func (a *Agent) GetServer() *http.Server {
	return a.httpServer
}

// Run runs all of agent UDP and HTTP servers in separate go-routines.
// It returns an error when it's immediately apparent on startup, but
// any errors happening after starting the servers are only logged.
func (a *Agent) Run() error {
    // 监听http端口
	listener, err := net.Listen("tcp", a.httpServer.Addr)
	if err != nil {
		return err
	}
    // 存储地址
	a.httpAddr.Store(listener.Addr().String())
	a.closer = listener
	go func() {
		a.logger.Info("Starting jaeger-agent HTTP server", zap.Int("http-port", listener.Addr().(*net.TCPAddr).Port))
        // 启动http server
		if err := a.httpServer.Serve(listener); err != nil {
			a.logger.Error("http server failure", zap.Error(err))
		}
		a.logger.Info("agent's http server exiting")
	}()

    // 启动3个goroutine
	for _, processor := range a.processors {
		go processor.Serve()
	}
	return nil
}

// HTTPAddr returns the address that HTTP server is listening on
func (a *Agent) HTTPAddr() string {
	return a.httpAddr.Load().(string)
}

// Stop forces all agent go routines to exit.
func (a *Agent) Stop() {
	for _, processor := range a.processors {
		go processor.Stop()
	}
	a.closer.Close()
}
