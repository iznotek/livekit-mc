// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ggwhite/go-masker"
	"github.com/urfave/cli/v2"

	"github.com/livekit/livekit-cli/pkg/config"
)

var (
	roomFlag = &cli.StringFlag{
		Name:     "room",
		Usage:    "name of the room",
		Required: false,
	}
	urlFlag = &cli.StringFlag{
		Name:    "url",
		Usage:   "url to LiveKit instance",
		EnvVars: []string{"LIVEKIT_URL"},
		Value:   "http://localhost:7880",
	}
	tokenFlag = &cli.StringFlag{
		Name:    "token",
		EnvVars: []string{"LIVEKIT_TOKEN"},
	}
	apiKeyFlag = &cli.StringFlag{
		Name:    "api-key",
		EnvVars: []string{"LIVEKIT_API_KEY"},
	}
	secretFlag = &cli.StringFlag{
		Name:    "api-secret",
		EnvVars: []string{"LIVEKIT_API_SECRET"},
	}
	identityFlag = &cli.StringFlag{
		Name:     "identity",
		Usage:    "identity of participant",
		Required: false,
	}
	projectFlag = &cli.StringFlag{
		Name:     "project",
		Usage:    "name of a configured project",
	}
	verboseFlag = &cli.BoolFlag{
		Name:     "verbose",
		Required: false,
	}
)

func withDefaultFlags(flags ...cli.Flag) []cli.Flag {
	return append([]cli.Flag{
		urlFlag,
		tokenFlag,
		apiKeyFlag,
		secretFlag,
		projectFlag,
		verboseFlag,
	}, flags...)
}

func PrintJSON(obj interface{}) {
	txt, _ := json.MarshalIndent(obj, "", "  ")
	fmt.Println(string(txt))
}

func ExpandUser(p string) string {
	if strings.HasPrefix(p, "~") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[1:])
	}

	return p
}

type loadParams struct {
	requireURL bool
}

type loadOption func(*loadParams)

var ignoreURL = func(p *loadParams) {
	p.requireURL = false
}

// attempt to load connection config, it'll prioritize
// 1. command line flags (or env var)
// 2. default project config
func loadProjectDetails(c *cli.Context, opts ...loadOption) (*config.ProjectConfig, error) {
	p := loadParams{requireURL: true}
	for _, opt := range opts {
		opt(&p)
	}
	logDetails := func(c *cli.Context, pc *config.ProjectConfig) {
		if c.Bool("verbose") {
			fmt.Printf("URL: %s, api-key: %s, api-secret: %s\n",
				pc.URL,
				pc.APIKey,
				masker.ID(pc.APISecret))
		}

	}

	// if explicit project is defined, then use it
	if c.String("project") != "" {
		pc, err := config.LoadProject(c.String("project"))
		if err != nil {
			return nil, err
		}
		fmt.Println("Using project:", c.String("project"))
		logDetails(c, pc)
		return pc, nil
	}

	pc := &config.ProjectConfig{}
	if val := c.String("url"); val != "" {
		pc.URL = val
	}
	if val := c.String("token"); val != "" {
		pc.Token = val
	}
	if val := c.String("api-key"); val != "" {
		pc.APIKey = val
	}
	if val := c.String("api-secret"); val != "" {
		pc.APISecret = val
	}
	if pc.Token != "" && (pc.URL != "" || !p.requireURL) {
		var envVars []string
		// if it's set via env, we should let users know
		if os.Getenv("LIVEKIT_URL") == pc.URL && pc.URL != "" {
			envVars = append(envVars, "url")
		}
		if os.Getenv("LIVEKIT_TOKEN") == pc.Token {
			envVars = append(envVars, "token")
		}
		if len(envVars) > 0 {
			fmt.Printf("Using %s from environment\n", strings.Join(envVars, ", "))
			logDetails(c, pc)
		}
		return pc, nil
	}
	if pc.APIKey != "" && pc.APISecret != "" && (pc.URL != "" || !p.requireURL) {
		var envVars []string
		// if it's set via env, we should let users know
		if os.Getenv("LIVEKIT_URL") == pc.URL && pc.URL != "" {
			envVars = append(envVars, "url")
		}
		if os.Getenv("LIVEKIT_API_KEY") == pc.APIKey {
			envVars = append(envVars, "api-key")
		}
		if os.Getenv("LIVEKIT_API_SECRET") == pc.APISecret {
			envVars = append(envVars, "api-secret")
		}
		if len(envVars) > 0 {
			fmt.Printf("Using %s from environment\n", strings.Join(envVars, ", "))
			logDetails(c, pc)
		}
		return pc, nil
	}

	// load default project
	dp, err := config.LoadDefaultProject()
	if err == nil {
		fmt.Println("Using default project", dp.Name)
		logDetails(c, dp)
		return dp, nil
	}

	if p.requireURL && pc.URL == "" {
		return nil, errors.New("url is required")
	}
	if pc.Token == "" && pc.APIKey == "" && pc.APISecret == "" {
		return nil, errors.New("token is required")
	}
	if pc.APIKey == "" {
		return nil, errors.New("api-key is required")
	}
	if pc.APISecret == "" {
		return nil, errors.New("api-secret is required")
	}

	// cannot happen
	return pc, nil
}
