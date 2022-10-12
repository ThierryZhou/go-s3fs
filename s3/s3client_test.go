package s3fs

import (
	"bytes"
	"context"
	"os"
	"testing"

	log "github.com/sirupsen/logrus"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

const (
	TestUser      string = "test-user"
	TestBucket    string = "test-bucket"
	TestAccessKey string = "test-user@tuya.com"
	TestSecretKey string = "test-nonexistent-user@tuya.com"
)

var (
	testClient Client
	testConfig = []byte(`
listen: 127.0.0.1:30001
dsn: root:123456@tcp(ai19:3306)/datakeeper?charset=utf8mb4&parseTime=true
kube-config: $HOME/.kube/config
run-in-cluster: false
`)
)

func s3client_setup(t *testing.T) {
	t.Logf("Before all tests")

	log.SetOutput(os.Stdout)

	viper.SetConfigType("yaml")
	viper.ReadConfig(bytes.NewBuffer(testConfig))
	o := ParseOption(viper.GetViper())
	log.Printf("options: %#v", o)

	assert := assert.New(t)

	_, err := NewClient(o)
	assert.NotNil(err)
}

func s3client_teardown(t *testing.T) {
	t.Logf("After all tests")
}

func Test_Buckets(t *testing.T) {

	s3client_setup(t)

	defer s3client_teardown(t)

	testCase1 := []struct {
		name    string
		user    string
		bucket  string
		wantErr bool
	}{
		// TODO: Add test cases.
		{
			name:    "true",
			user:    TestUser,
			bucket:  TestBucket,
			wantErr: false,
		},
	}
	for _, tt := range testCase1 {
		t.Run(tt.name, func(t *testing.T) {
			_, err := testClient.CreateBucket(context.TODO(), tt.user, tt.bucket)
			var isErr bool
			if err != nil {
				isErr = true
			} else {
				isErr = false
			}
			assert.Equal(t, isErr, tt.wantErr, "test case is failed.")
		})
	}

	testCase2 := []struct {
		name    string
		user    string
		bucket  string
		object  []string
		wantErr bool
	}{
		// TODO: Add test cases.
		{
			name:    "true",
			user:    TestUser,
			bucket:  TestBucket,
			wantErr: false,
		},
	}
	for _, tt := range testCase2 {
		t.Run(tt.name, func(t *testing.T) {
			_, err := testClient.CreateBucket(context.TODO(), tt.user, tt.bucket)
			var isErr bool
			if err != nil {
				isErr = true
			} else {
				isErr = false
			}
			assert.Equal(t, isErr, tt.wantErr, "test case is failed.")
		})
	}
}
