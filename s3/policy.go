// Copyright 2022 the go-s3fs Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// recently only refer read/write priviledges

package s3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	s3v2 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	log "github.com/sirupsen/logrus"
)

var (
	rwDirActionSet = []string{
		"s3:GetBucketLocation",
		"s3:ListBucket",
		"s3:DeleteObject",
		"s3:ListBucketMultipartUploads",
	}

	roDirActionSet = []string{
		"s3:GetBucketLocation",
		"s3:ListBucket",
		"s3:ListBucketMultipartUploads",
	}

	rwObjActionSet = []string{
		"s3:AbortMultipartUpload",
		"s3:GetObject",
		"s3:DeleteObject",
		"s3:ListMultipartUploadParts",
		"s3:PutObject",
	}

	roObjActionSet = []string{
		"s3:AbortMultipartUpload",
		"s3:GetObject",
		"s3:ListMultipartUploadParts",
	}
)

type s3Policy struct {
	Mod int32
}

type DirPolicy struct {
	name   string
	owner  string
	shares []string
}

type BucketPolicy struct {
	bucket string
	dirs   map[string]DirPolicy
	owner  string
	shares []string
}

func NewBucketPolicy(bucket, user string) *BucketPolicy {
	return &BucketPolicy{
		bucket: bucket,
		owner:  user,
	}
}

func (p *BucketPolicy) AddOwnDir(dir, user string) {
	p.dirs[dir] = DirPolicy{
		name:  dir,
		owner: user,
	}
}

func (p *BucketPolicy) RemoveOwnDir(dir, user string) {
	delete(p.dirs, dir)
}

func (p *BucketPolicy) AddShareDir(dir, user string) {
	d := p.dirs[dir]
	p.shares = append(d.shares, user)
}

func (p *BucketPolicy) RemoveShareDir(dir, user string) {
	index := 0
	d := p.dirs[dir]
	for _, i := range d.shares {
		if i != user {
			d.shares[index] = i
			index++
		}
	}
	d.shares = d.shares[:index]
}

func (p *BucketPolicy) AddShare(user string) {
	p.shares = append(p.shares, user)
}

func (p *BucketPolicy) RemoveShare(user string) {
	index := 0
	for _, i := range p.shares {
		if i != user {
			p.shares[index] = i
			index++
		}
	}
	p.shares = p.shares[:index]
}

func (p *BucketPolicy) ToString() string {

	policies := map[string]interface{}{
		"Version":   "2012-10-17",
		"Statement": append(p.BucketPolicy(), p.DirsPolicy()...),
	}

	b, err := json.Marshal(policies)
	if err != nil {
		return ""
	}

	return string(b)
}

func (p *BucketPolicy) BucketPolicy() []map[string]interface{} {
	ownPrinc := []string{fmt.Sprintf("arn:aws:iam:::user/%s", p.owner)}

	var sharePrinc []string
	for _, u := range p.shares {
		sharePrinc = append(sharePrinc, fmt.Sprintf("arn:aws:iam:::user/%s", u))
	}

	bucketRes := []string{fmt.Sprintf("arn:aws:s3:::%s", p.bucket)}
	objectRes := []string{fmt.Sprintf("arn:aws:s3:::%s/*", p.bucket)}

	bucketOwnerStatement := map[string]interface{}{
		"Effect": "Allow",
		"Principal": map[string]interface{}{
			"AWS": ownPrinc,
		},
		"Action":   rwDirActionSet,
		"Resource": bucketRes,
	}

	objectOwnerStatement := map[string]interface{}{
		"Effect": "Allow",
		"Principal": map[string]interface{}{
			"AWS": ownPrinc,
		},
		"Action":   rwObjActionSet,
		"Resource": objectRes,
	}

	if len(sharePrinc) > 0 {

		bucketShareStatement := map[string]interface{}{
			"Effect": "Allow",
			"Principal": map[string]interface{}{
				"AWS": sharePrinc,
			},
			"Action":   roDirActionSet,
			"Resource": bucketRes,
		}

		objectShareStatement := map[string]interface{}{
			"Effect": "Allow",
			"Principal": map[string]interface{}{
				"AWS": sharePrinc,
			},
			"Action":   roObjActionSet,
			"Resource": objectRes,
		}

		return []map[string]interface{}{
			bucketOwnerStatement,
			objectOwnerStatement,
			bucketShareStatement,
			objectShareStatement,
		}
	} else {
		return []map[string]interface{}{
			bucketOwnerStatement,
			objectOwnerStatement,
		}
	}
}

func (p *BucketPolicy) DirsPolicy() []map[string]interface{} {

	var dirPolicies []map[string]interface{}
	for k, v := range p.dirs {
		dir := k
		ownPrinc := []string{fmt.Sprintf("arn:aws:iam:::user/%s", v.owner)}
		var sharePrinc []string
		for _, u := range p.shares {
			sharePrinc = append(sharePrinc, fmt.Sprintf("arn:aws:iam:::user/%s", u))
		}

		dirRes := []string{fmt.Sprintf("arn:aws:s3:::%s/%s", p.bucket, dir)}
		objRes := []string{fmt.Sprintf("arn:aws:s3:::%s/%s/*", p.bucket, dir)}

		dirOwnerStatement := map[string]interface{}{
			"Effect": "Allow",
			"Principal": map[string]interface{}{
				"AWS": ownPrinc,
			},
			"Action":   rwDirActionSet,
			"Resource": dirRes,
		}

		objOwnerStatement := map[string]interface{}{
			"Effect": "Allow",
			"Principal": map[string]interface{}{
				"AWS": ownPrinc,
			},
			"Action":   rwObjActionSet,
			"Resource": objRes,
		}

		if len(sharePrinc) > 0 {

			dirShareStatement := map[string]interface{}{
				"Effect": "Allow",
				"Principal": map[string]interface{}{
					"AWS": sharePrinc,
				},
				"Action":   roDirActionSet,
				"Resource": dirRes,
			}

			objShareStatement := map[string]interface{}{
				"Effect": "Allow",
				"Principal": map[string]interface{}{
					"AWS": sharePrinc,
				},
				"Action":   roObjActionSet,
				"Resource": objRes,
			}

			dirPolicies = append(dirPolicies, dirOwnerStatement, objOwnerStatement, dirShareStatement, objShareStatement)
		} else {
			dirPolicies = append(dirPolicies, dirOwnerStatement, objOwnerStatement)
		}
	}

	return dirPolicies
}

func (c *s3Client) PutBucketPolicy(ctx context.Context, bucket, policy string) error {

	_, err := c.client.PutBucketPolicy(ctx, &s3v2.PutBucketPolicyInput{
		Bucket: aws.String(bucket),
		Policy: aws.String(policy),
	})

	if err != nil {
		var nsb *types.NoSuchBucket
		if errors.As(err, &nsb) {
			log.Warn("NoSuchBucket")
			return err
		}
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			log.Warn(apiErr.ErrorMessage())
			// handle error code
			return err
		}
		// handle error
		return err
	}

	return nil
}

func (c *s3Client) DeleteBucketPolicy(ctx context.Context, bucket string) error {
	_, err := c.client.DeleteBucketPolicy(ctx, &s3v2.DeleteBucketPolicyInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		var nsb *types.NoSuchBucket
		if errors.As(err, &nsb) {
			log.Warn("NoSuchBucket")
			return err
		}
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			log.Warn(apiErr.ErrorMessage())
			// handle error code
			return err
		}
		// handle error
		return err
	}

	return nil
}

func (c *s3Client) GetBucketPolicy(ctx context.Context, bucket string) (string, error) {

	p, err := c.client.GetBucketPolicy(ctx, &s3v2.GetBucketPolicyInput{
		Bucket: aws.String(bucket),
	})

	if err != nil {
		var nsb *types.NoSuchBucket
		if errors.As(err, &nsb) {
			log.Warn("NoSuchBucket")
			return "", err
		}
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			log.Warn(apiErr.ErrorMessage())
			// handle error code
			return "", err
		}
		// handle error
		return "", err
	}

	return *p.Policy, nil
}

func (c *s3Client) GenerateBucketPolicy(ctx context.Context, bucket, owner string, to_users []string) (string, error) {
	p := NewBucketPolicy(bucket, owner)
	for _, itor := range to_users {
		p.AddShare(itor)
	}

	return p.ToString(), nil
}
