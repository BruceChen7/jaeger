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

package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	jMetrics "github.com/uber/jaeger-lib/metrics"
	"go.uber.org/zap"
	"google.golang.org/grpc/grpclog"

	"github.com/jaegertracing/jaeger/cmd/agent/app"
	"github.com/jaegertracing/jaeger/cmd/agent/app/reporter"
	"github.com/jaegertracing/jaeger/cmd/agent/app/reporter/grpc"
	"github.com/jaegertracing/jaeger/cmd/agent/app/reporter/tchannel"
	"github.com/jaegertracing/jaeger/cmd/flags"
	"github.com/jaegertracing/jaeger/pkg/config"
	"github.com/jaegertracing/jaeger/pkg/metrics"
	"github.com/jaegertracing/jaeger/pkg/version"
)

func main() {
	var signalsChannel = make(chan os.Signal)
    // 将interrupt，和SIGTERM信号捕捉到signalsChannel
	signal.Notify(signalsChannel, os.Interrupt, syscall.SIGTERM)

    // 加载配置
	v := viper.New()
	var command = &cobra.Command{
		Use:   "jaeger-agent",
		Short: "Jaeger agent is a local daemon program which collects tracing data.",
		Long:  `Jaeger agent is a daemon program that runs on every host and receives tracing data submitted by Jaeger client libraries.`,
		RunE: func(cmd *cobra.Command, args []string) error {
            // 加载配置文件
			err := flags.TryLoadConfigFile(v)
			if err != nil {
				return err
			}

            // 从配置中设置log level和心跳检查服务到端口
			sFlags := new(flags.SharedFlags).InitFromViper(v)
            // 按照配置创建logger
			logger, err := sFlags.NewLogger(zap.NewProductionConfig())
			if err != nil {
				return err
			}

            // 从配置中设置worker数量，queuesize，和端口号
			builder := new(app.Builder).InitFromViper(v)
			mBldr := new(metrics.Builder).InitFromViper(v)

			mFactory, err := mBldr.CreateMetricsFactory("jaeger")
			if err != nil {
				logger.Fatal("Could not create metrics", zap.Error(err))
			}
            // 设置后端的处理
			mFactory = mFactory.Namespace(jMetrics.NSOptions{Name: "agent", Tags: nil})

			rOpts := new(reporter.Options).InitFromViper(v)
            // tChan的设置
			tChanOpts := new(tchannel.Builder).InitFromViper(v, logger)
            // grpc的设置
			grpcOpts := new(grpc.Options).InitFromViper(v)
            // 创建collector proxy
			cp, err := createCollectorProxy(rOpts, tChanOpts, grpcOpts, logger, mFactory)
			if err != nil {
				logger.Fatal("Could not create collector proxy", zap.Error(err))
			}

			// TODO illustrate discovery service wiring

            // 创建agent
			agent, err := builder.CreateAgent(cp, logger, mFactory)
			if err != nil {
                // wrap住错误
				return errors.Wrap(err, "Unable to initialize Jaeger Agent")
			}

            // 注册metrics handler
			if h := mBldr.Handler(); mFactory != nil && h != nil {
				logger.Info("Registering metrics handler with HTTP server", zap.String("route", mBldr.HTTPRoute))
				agent.GetServer().Handler.(*http.ServeMux).Handle(mBldr.HTTPRoute, h)
			}

			logger.Info("Starting agent")
			if err := agent.Run(); err != nil {
				return errors.Wrap(err, "Failed to run the agent")
			}
            // 读取信号
			<-signalsChannel
            // 关闭
			logger.Info("Shutting down")
			if closer, ok := cp.(io.Closer); ok {
				closer.Close()
			}
			logger.Info("Shutdown complete")
			return nil
		},
	}

	command.AddCommand(version.Command())

	config.AddFlags(
		v,
		command,
		flags.AddConfigFileFlag,
		flags.AddLoggingFlag,
		app.AddFlags,
		reporter.AddFlags,
		tchannel.AddFlags,
		grpc.AddFlags,
		metrics.AddFlags,
	)

	if err := command.Execute(); err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
}

func createCollectorProxy(
	opts *reporter.Options,
	tchanRep *tchannel.Builder,
	grpcRepOpts *grpc.Options,
	logger *zap.Logger,
	mFactory jMetrics.Factory,
) (app.CollectorProxy, error) {
    // 根据上报到类型，来创建proxy
	switch opts.ReporterType {
	case reporter.GRPC:
		grpclog.SetLoggerV2(grpclog.NewLoggerV2(ioutil.Discard, os.Stderr, os.Stderr))
		return grpc.NewCollectorProxy(grpcRepOpts, mFactory, logger)
	case reporter.TCHANNEL:
		return tchannel.NewCollectorProxy(tchanRep, mFactory, logger)
	default:
		return nil, errors.New(fmt.Sprintf("unknown reporter type %s", string(opts.ReporterType)))
	}
}
