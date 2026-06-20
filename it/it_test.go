/*
Copyright (c) 2026 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with
the License. You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the
specific language governing permissions and limitations under the License.
*/

package it

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2/dsl/core"
	. "github.com/onsi/ginkgo/v2/dsl/table"
	. "github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"
)

func TestIT(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration tests")
}

var (
	logger     *slog.Logger
	workDir    string
	projectDir string
	protoDir   string
	pluginBin  string
)

var _ = BeforeSuite(func() {
	var err error

	// Create a logger:
	handler := slog.NewJSONHandler(GinkgoWriter, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger = slog.New(handler)

	// Get the project root directory:
	workDir, err := os.Getwd()
	Expect(err).ToNot(HaveOccurred())
	projectDir = filepath.Dir(workDir)

	// Calculate the path of the binary of the plugion:
	pluginBin = filepath.Join(projectDir, "protoc-gen-cleanapi")

	// Build the plugin.
	cmd := exec.Command(
		"go", "build",
		"-gcflags=all=-N -l",
		"-o", pluginBin,
	)
	cmd.Dir = projectDir
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	err = cmd.Run()
	Expect(err).ToNot(HaveOccurred())

	// Calculate the path of the proto directory:
	protoDir = filepath.Join(projectDir, "proto")
})

var _ = DescribeTable(
	"Behaviour",
	func(caseDir string) {
		// Calculate the full path of the case directory:
		caseDir = filepath.Join(workDir, "cases", caseDir)
		_, err := os.Stat(caseDir)
		Expect(err).ToNot(HaveOccurred())

		// Calculate the input and output directories:
		inputDir := filepath.Join(caseDir, "input")
		_, err = os.Stat(inputDir)
		Expect(err).ToNot(HaveOccurred())
		outputDir := filepath.Join(caseDir, "output")
		_, err = os.Stat(outputDir)
		Expect(err).ToNot(HaveOccurred())

		// Create the workspace directory:
		wsDir, err := os.MkdirTemp("", "*.ws")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() {
			err := os.RemoveAll(wsDir)
			Expect(err).ToNot(HaveOccurred())
		})

		// Copy the input files to the workspace:
		wsInputDir := filepath.Join(wsDir, "input")
		err = os.MkdirAll(wsInputDir, 0755)
		Expect(err).ToNot(HaveOccurred())
		err = os.CopyFS(wsInputDir, os.DirFS(inputDir))
		Expect(err).ToNot(HaveOccurred())

		// Copy the API files to the workspace:
		wsApiDir := filepath.Join(wsDir, "api")
		err = os.CopyFS(wsApiDir, os.DirFS(protoDir))
		Expect(err).ToNot(HaveOccurred())

		// Create temporary output directory:
		wsOutputDir := filepath.Join(wsDir, "output")
		err = os.MkdirAll(wsOutputDir, 0755)
		Expect(err).ToNot(HaveOccurred())

		// Generate the 'buf.yaml' file of the workspace:
		wsBufData := map[string]any{
			"version": "v2",
			"modules": []any{
				map[string]any{
					"name": "buf.build/cleanapi/input",
					"path": filepath.Base(wsInputDir),
				},
				map[string]any{
					"name": "buf.build/cleanapi/cleanapi",
					"path": filepath.Base(wsApiDir),
				},
			},
			"deps": []any{
				"buf.build/googleapis/googleapis",
			},
		}
		wsBufBytes, err := yaml.Marshal(wsBufData)
		Expect(err).ToNot(HaveOccurred())
		wsBufFile := filepath.Join(wsDir, "buf.yaml")
		err = os.WriteFile(wsBufFile, wsBufBytes, 0600)
		Expect(err).ToNot(HaveOccurred())
		logger.Debug(
			"Generated 'buf.yaml' file for the workspace",
			slog.Any("data", wsBufData),
			slog.String("file", wsBufFile),
			slog.String("bytes", string(wsBufBytes)),
		)

		// Generate the 'buf.gen.yaml' file of the workspace:
		wsBufGen := map[string]any{
			"version": "v2",
			"inputs": []any{
				map[string]any{
					"directory": filepath.Base(wsInputDir),
				},
			},
			"plugins": []any{
				map[string]any{
					"local": pluginBin,
					"out":   wsOutputDir,
					"opt": []any{
						fmt.Sprintf("proto_root=%s", wsInputDir),
					},
				},
			},
		}
		wsBufGenBytes, err := yaml.Marshal(wsBufGen)
		Expect(err).ToNot(HaveOccurred())
		wsBufGenFile := filepath.Join(wsDir, "buf.gen.yaml")
		err = os.WriteFile(wsBufGenFile, wsBufGenBytes, 0600)
		Expect(err).ToNot(HaveOccurred())
		logger.Debug(
			"Generated 'buf.gen.yaml' file for the workspace",
			slog.Any("data", wsBufGen),
			slog.String("file", wsBufGenFile),
			slog.String("bytes", string(wsBufGenBytes)),
		)

		// Run 'buf dep update' to update the dependencies:
		bufCmd := exec.Command("buf", "dep", "update")
		bufCmd.Dir = wsDir
		bufCmd.Stdout = GinkgoWriter
		bufCmd.Stderr = GinkgoWriter
		err = bufCmd.Run()
		Expect(err).ToNot(HaveOccurred())

		// Run 'buf generate' to generate the output files:
		bufCmd = exec.Command("buf", "generate")
		bufCmd.Dir = wsDir
		bufCmd.Stdout = GinkgoWriter
		bufCmd.Stderr = GinkgoWriter
		err = bufCmd.Run()
		Expect(err).ToNot(HaveOccurred())

		// Compare the expected and actual output files using the 'diff' command:
		diffCmd := exec.Command(
			"diff",
			"--recursive",
			"--unified",
			"--new-file",
			outputDir,
			wsOutputDir,
		)
		diffCmd.Stdout = GinkgoWriter
		diffCmd.Stderr = GinkgoWriter
		err = diffCmd.Run()
		Expect(err).ToNot(HaveOccurred())
	},
	Entry(
		"Basic filtering",
		"basic_filtering",
	),
	Entry(
		"Comment preservation",
		"comment_preservation",
	),
	Entry(
		"HTTP transcoding removal",
		"http_transcoding_removal",
	),
	Entry(
		"Cross package references",
		"cross_package_references",
	),
	Entry(
		"Multiple packages",
		"multiple_packages",
	),
	Entry(
		"Multiple private elements",
		"multiple_private_elements",
	),
	Entry(
		"Private file",
		"private_file",
	),
	Entry(
		"Import of private file",
		"import_of_private_file",
	),
)
