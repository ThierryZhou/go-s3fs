// Copyright 2022 the go-s3fs Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// recently only refer read/write priviledges

package s3

import (
	"strings"
)

type Option struct {
	URL         string `json:"url"`
	ExternalURL string `json:"external-url"`
	Region      string `json:"region"`
	AccessKey   string `json:"accesskey"`
	SecretKey   string `json:"secretkey"`
	Token       string `json:"token"`
}

var (
	defaultOption = Option{
		URL:         "http://s3-endpoint:8000",
		ExternalURL: "https://minio.io:9000",
		Region:      "us-east-1",
		AccessKey:   "minio",
		SecretKey:   "minio111",
		Token:       "",
	}
)

type OptionFunc func(*Option)

func WithS3Address(endpoint string, secure bool) OptionFunc {
	return func(o *Option) {
		o.URL = endpoint
	}
}

func WithS3User(userID, token string) OptionFunc {
	return func(o *Option) {
		o.Token = token
	}
}

func ParseOption(args string) *Option {

	o := Option{}

	entries := strings.Split(args, ",")
	for _, e := range entries {
		parts := strings.Split(e, "=")
		if parts[0] == "url" {
			o.URL = parts[1]
		} else if parts[0] == "accesskey" {
			o.AccessKey = parts[1]
		} else if parts[0] == "secretkey" {
			o.SecretKey = parts[1]
		}
	}

	return &o
}
