// MIT License

// Copyright (c) [2022] [Bohdan Ivashko (https://github.com/Arriven)]

// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:

// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.

// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package main

import (
	"context"
	"flag"
	"math/rand"
	"net/http"
	pprofhttp "net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/Arriven/db1000n/src/job"
	"github.com/Arriven/db1000n/src/job/config"
	"github.com/Arriven/db1000n/src/utils"
	"github.com/Arriven/db1000n/src/utils/metrics"
	"github.com/Arriven/db1000n/src/utils/ota"
	"github.com/Arriven/db1000n/src/utils/templates"
)

func main() {
	runnerConfigOptions := job.NewConfigOptionsWithFlags()
	jobsGlobalConfig := job.NewGlobalConfigWithFlags()
	otaConfig := ota.NewConfigWithFlags()
	countryCheckerConfig := utils.NewCountryCheckerConfigWithFlags()
	updaterMode, destinationPath := config.NewUpdaterOptionsWithFlags()
	prometheusOn, prometheusListenAddress, prometheusPushGateways := metrics.NewOptionsWithFlags()
	pprof := flag.String("pprof", utils.GetEnvStringDefault("GO_PPROF_ENDPOINT", ""), "enable pprof")
	help := flag.Bool("h", false, "print help message and exit")
	version := flag.Bool("version", false, "print version and exit")
	debug := flag.Bool("debug", utils.GetEnvBoolDefault("DEBUG", false), "enable debug level logging and features")
	verbose := flag.Int("verbose", utils.GetEnvIntDefault("VERBOSE", 1),
		"verbosity level, 0 - disable most logs, 1 - standard level of logging, 2 - debug logs")

	flag.Parse()

	if *debug {
		*verbose = 2
	}

	logger, err := newZapLogger(*verbose)
	if err != nil {
		panic(err)
	}

	logger.Info("running db1000n", zap.String("version", ota.Version), zap.Int("pid", os.Getpid()))

	switch {
	case *help:
		flag.CommandLine.Usage()

		return
	case *version:
		return
	case *updaterMode:
		config.UpdateLocal(logger, *destinationPath, strings.Split(runnerConfigOptions.PathsCSV, ","), []byte(runnerConfigOptions.BackupConfig))

		return
	}

	err = utils.UpdateRLimit(logger)
	if err != nil {
		logger.Warn("failed to increase rlimit", zap.Error(err))
	}

	go ota.WatchUpdates(logger, otaConfig)
	setUpPprof(logger, *pprof, *debug)
	rand.Seed(time.Now().UnixNano())

	country := utils.CheckCountryOrFail(logger, countryCheckerConfig, templates.ParseAndExecute(logger, jobsGlobalConfig.ProxyURLs, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	metrics.InitOrFail(ctx, logger, *prometheusOn, *prometheusListenAddress, *prometheusPushGateways, jobsGlobalConfig.ClientID, country)

	go cancelOnSignal(logger, cancel)
	job.NewRunner(runnerConfigOptions, jobsGlobalConfig).Run(ctx, logger)
}

func newZapLogger(verbose int) (*zap.Logger, error) {
	switch verbose {
	case 0:
		return zap.NewNop(), nil
	case 1:
		return zap.NewProduction()
	default:
		return zap.NewDevelopment()
	}
}

func setUpPprof(logger *zap.Logger, pprof string, debug bool) {
	switch {
	case debug && pprof == "":
		pprof = ":8080"
	case pprof == "":
		return
	}

	mux := http.NewServeMux()
	mux.Handle("/debug/pprof/", http.HandlerFunc(pprofhttp.Index))
	mux.Handle("/debug/pprof/cmdline", http.HandlerFunc(pprofhttp.Cmdline))
	mux.Handle("/debug/pprof/profile", http.HandlerFunc(pprofhttp.Profile))
	mux.Handle("/debug/pprof/symbol", http.HandlerFunc(pprofhttp.Symbol))
	mux.Handle("/debug/pprof/trace", http.HandlerFunc(pprofhttp.Trace))

	// this has to be wrapped into a lambda bc otherwise it blocks when evaluating argument for zap.Error
	go func() { logger.Warn("pprof server", zap.Error(http.ListenAndServe(pprof, mux))) }()
}

func cancelOnSignal(logger *zap.Logger, cancel context.CancelFunc) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs,
		syscall.SIGTERM,
		syscall.SIGABRT,
		syscall.SIGHUP,
		syscall.SIGINT,
	)
	<-sigs
	logger.Info("terminating")
	cancel()
}
