package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"lark-agent-bridge/internal/e2e"
	"lark-agent-bridge/internal/e2e/commanddriver"
)

var errScenarioFailed = errors.New("E2E scenario failed")

type driverFactory func(e2e.Config, e2e.Deployment) e2e.Drivers
type runIDFactory func() (string, error)

func main() {
	err := run(os.Args[1:], os.Stdout, commanddriver.NewDrivers, newRunID)
	if err == nil {
		return
	}
	if !errors.Is(err, errScenarioFailed) {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(1)
}

func run(args []string, stdout io.Writer, drivers driverFactory, ids runIDFactory) error {
	if len(args) == 0 || args[0] != "run" {
		return fmt.Errorf("usage: lark-bridge-e2e run --scenario <name> --deployment <path> --config <path> --evidence-dir <path>")
	}
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	scenarioName := flags.String("scenario", "", "registered scenario name")
	deploymentPath := flags.String("deployment", "", "deployment descriptor JSON")
	configPath := flags.String("config", "", "runner configuration JSON")
	evidenceRoot := flags.String("evidence-dir", "", "evidence output directory")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *scenarioName == "" || *deploymentPath == "" || *configPath == "" || *evidenceRoot == "" {
		return fmt.Errorf("scenario, deployment, config, and evidence-dir are required")
	}

	deployment, err := e2e.LoadDeployment(*deploymentPath)
	if err != nil {
		return err
	}
	config, err := e2e.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	runID, err := ids()
	if err != nil {
		return fmt.Errorf("generate run id: %w", err)
	}
	evidence, err := e2e.NewEvidence(*evidenceRoot, runID, deployment, *scenarioName)
	if err != nil {
		return err
	}

	registry := e2e.Registry{"stop": e2e.StopScenario(), "resume": e2e.ResumeScenario()}
	scenario, failure := registry.Resolve(*scenarioName)
	if failure != nil {
		now := time.Now()
		result := e2e.Result{
			SchemaVersion: 1,
			Scenario:      *scenarioName,
			Status:        "failed",
			StartedAt:     now,
			FinishedAt:    now,
			Failure:       failure,
		}
		if err := evidence.Finish(result); err != nil {
			return err
		}
		printFailure(stdout, result, evidence.Dir())
		return errScenarioFailed
	}

	runContext := &e2e.RunContext{
		Deployment: deployment,
		Config:     config,
		Evidence:   evidence,
		Drivers:    drivers(config, deployment),
		Values:     make(map[string]string),
	}
	result := e2e.NewRunner().Run(context.Background(), scenario, runContext)
	if result.Status == "passed" {
		fmt.Fprintf(stdout, "PASS scenario=%s evidence=%s\n", result.Scenario, evidence.Dir())
		return nil
	}
	printFailure(stdout, result, evidence.Dir())
	return errScenarioFailed
}

func printFailure(stdout io.Writer, result e2e.Result, evidenceDir string) {
	failure := result.SummaryFailure()
	if failure == nil {
		failure = &e2e.Failure{Class: e2e.FailureHarness, Step: "summarize_result"}
	}
	fmt.Fprintf(stdout, "FAIL class=%s scenario=%s step=%s evidence=%s\n", failure.Class, result.Scenario, failure.Step, evidenceDir)
}

func newRunID() (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("e2e-%s-%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(random[:])), nil
}
